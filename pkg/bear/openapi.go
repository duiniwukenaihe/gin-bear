package bear

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/gin-gonic/gin"
)

// RouteMetadata 记录路由元数据用于生成文档
type RouteMetadata struct {
	Method      string
	Path        string
	GroupName   string
	HandlerType reflect.Type
	HandlerName string
}

// OpenAPIInfo 接口描述元数据
type OpenAPIInfo struct {
	Summary     string
	Description string
	Tags        []string
}

// IOpenAPI 控制器可实现此接口以提供更多文档信息
type IOpenAPI interface {
	OpenAPI() map[string]OpenAPIInfo // path -> info
}

const openAPIRouteMetadataStoreKey = "gin-bear.internal.openapi-route-metadata"

type openAPIRouteMetadata struct {
	fullPath   string
	controller IOpenAPI
	fairings   []Fairing
}

type openAPIRouteMetadataStore struct {
	mu     sync.RWMutex
	routes map[RouteMetadata]openAPIRouteMetadata
}

// setOpenAPIRouteMetadata is the package-private integration point for route
// registration. It snapshots instance identity and effective fairings.
func (b *Bear) setOpenAPIRouteMetadata(route RouteMetadata, fullPath string, controller IOpenAPI, fairings ...Fairing) {
	if b == nil {
		return
	}
	if b.exprData == nil {
		b.exprData = make(map[string]interface{})
	}
	store, _ := b.exprData[openAPIRouteMetadataStoreKey].(*openAPIRouteMetadataStore)
	if store == nil {
		store = &openAPIRouteMetadataStore{routes: make(map[RouteMetadata]openAPIRouteMetadata)}
		b.exprData[openAPIRouteMetadataStoreKey] = store
	}
	store.mu.Lock()
	store.routes[route] = openAPIRouteMetadata{
		fullPath:   fullPath,
		controller: controller,
		fairings:   append([]Fairing(nil), fairings...),
	}
	store.mu.Unlock()
}

func (b *Bear) openAPIRouteMetadata(route RouteMetadata) (openAPIRouteMetadata, bool) {
	if b == nil || b.exprData == nil {
		return openAPIRouteMetadata{}, false
	}
	store, _ := b.exprData[openAPIRouteMetadataStoreKey].(*openAPIRouteMetadataStore)
	if store == nil {
		return openAPIRouteMetadata{}, false
	}
	store.mu.RLock()
	metadata, ok := store.routes[route]
	store.mu.RUnlock()
	if ok {
		metadata.fairings = append([]Fairing(nil), metadata.fairings...)
	}
	return metadata, ok
}

// OpenAPISchema 简化的 OpenAPI 3.0 定义结构
type OpenAPISchema struct {
	OpenAPI    string                 `json:"openapi"`
	Info       map[string]interface{} `json:"info"`
	Paths      map[string]interface{} `json:"paths"`
	Components map[string]interface{} `json:"components"`
	Security   []map[string][]string  `json:"security,omitempty"`
}

// GenerateOpenAPI 生成 OpenAPI 3.0 文档内容
func (b *Bear) GenerateOpenAPI() ([]byte, error) {
	var config *SysConfig
	var routes []RouteMetadata
	if b != nil && b.runtime != nil {
		config = b.runtime.Config
	}
	if b != nil {
		routes = b.routeRegistry
	}
	title := "gin-bear"
	if config != nil && config.Server != nil && config.Server.Name != "" {
		title = config.Server.Name
	}
	componentSchemas := map[string]interface{}{
		"ErrorResponse": map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"code":    map[string]interface{}{"type": "integer"},
				"message": map[string]interface{}{"type": "string"},
				"data":    map[string]interface{}{"type": "object"},
			},
			"required": []string{"code", "message"},
		},
	}
	schemaBuilder := newOpenAPISchemaBuilder(componentSchemas)
	envelopeMode := config != nil && config.ResponseMode() == "envelope"
	schema := OpenAPISchema{
		OpenAPI: "3.0.0",
		Info: map[string]interface{}{
			"title":   title,
			"version": "1.0.0",
		},
		Paths: make(map[string]interface{}),
		Components: map[string]interface{}{
			"schemas": componentSchemas,
		},
	}
	globalAuth := openAPIHasAuthFairing(openAPIGlobalFairings(b))
	anyAuth := globalAuth
	if !anyAuth {
		for _, route := range routes {
			metadata, _ := b.openAPIRouteMetadata(route)
			if openAPIHasAuthFairing(metadata.fairings) {
				anyAuth = true
				break
			}
		}
	}
	if anyAuth {
		schema.Components["securitySchemes"] = map[string]interface{}{
			"BearerAuth": map[string]interface{}{
				"type":         "http",
				"scheme":       "bearer",
				"bearerFormat": "JWT",
			},
		}
		if globalAuth {
			schema.Security = []map[string][]string{
				{"BearerAuth": []string{}},
			}
		}
	}

	seenRoutes := make(map[string]RouteMetadata)
	seenOperationIDs := make(map[string]RouteMetadata)
	// 遍历路由元数据
	for _, route := range routes {
		method := strings.ToLower(strings.TrimSpace(route.Method))
		if method == "" {
			return nil, fmt.Errorf("openapi route %q missing method", route.Path)
		}
		if !openAPIMethodAllowed(method) {
			return nil, fmt.Errorf("openapi route %q has unsupported method %q", route.Path, route.Method)
		}
		if strings.TrimSpace(route.Path) == "" {
			return nil, fmt.Errorf("openapi route %q missing path", route.HandlerName)
		}
		baseOperationID := strings.TrimSpace(route.HandlerName)
		if baseOperationID == "" {
			return nil, fmt.Errorf("openapi route %s %s missing operationId", route.Method, route.Path)
		}

		metadata, _ := b.openAPIRouteMetadata(route)
		path := strings.TrimSpace(metadata.fullPath)
		if path == "" {
			path = route.Path
			if route.GroupName != "" && !strings.HasPrefix(path, "/"+route.GroupName) {
				path = "/" + route.GroupName + path
			}
		}
		// 统一路径格式
		path = strings.ReplaceAll(path, "//", "/")
		routeAuth := globalAuth || openAPIHasAuthFairing(metadata.fairings)
		publicRoute := routeAuth && openAPIRouteIsPublic(path, config)
		path = toOpenAPIPath(path)
		routeKey := method + " " + path
		if previous, exists := seenRoutes[routeKey]; exists {
			return nil, fmt.Errorf("duplicate route %s %s for %q and %q", strings.ToUpper(method), path, previous.HandlerName, route.HandlerName)
		}
		seenRoutes[routeKey] = route
		operationID, err := openAPIOperationID(route, method, path, seenOperationIDs)
		if err != nil {
			return nil, err
		}
		seenOperationIDs[operationID] = route

		if _, exists := schema.Paths[path]; !exists {
			schema.Paths[path] = make(map[string]interface{})
		}

		op := map[string]interface{}{
			"operationId": operationID,
			"responses": map[string]interface{}{
				"200": map[string]interface{}{
					"description": "OK",
				},
			},
		}
		enrichOpenAPIOperation(op, method, route.HandlerType, schemaBuilder, envelopeMode)
		ensureOpenAPIPathParameters(op, path)
		if publicRoute {
			op["security"] = []map[string][]string{}
		} else if routeAuth && !globalAuth {
			op["security"] = []map[string][]string{
				{"BearerAuth": []string{}},
			}
		}
		addStandardOpenAPIErrorResponses(op, routeAuth && !publicRoute)

		if info, ok := openAPIControllerInfo(metadata, route, path); ok {
			op["summary"] = info.Summary
			op["description"] = info.Description
			op["tags"] = info.Tags
		}

		schema.Paths[path].(map[string]interface{})[method] = op
	}

	document, err := json.MarshalIndent(schema, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal generated OpenAPI document: %w", err)
	}
	loader := openapi3.NewLoader()
	parsed, err := loader.LoadFromData(document)
	if err != nil {
		return nil, fmt.Errorf("parse generated OpenAPI document: %w", err)
	}
	if err := parsed.Validate(context.Background()); err != nil {
		return nil, fmt.Errorf("validate generated OpenAPI document: %w", err)
	}
	return document, nil
}

func openAPIGlobalFairings(b *Bear) []Fairing {
	if b == nil || b.fairingHandler == nil {
		return nil
	}
	return b.fairingHandler.requestFairings
}

func openAPIHasAuthFairing(fairings []Fairing) bool {
	for _, fairing := range fairings {
		if _, ok := fairing.(*AuthFairing); ok {
			return true
		}
	}
	return false
}

func openAPIControllerInfo(metadata openAPIRouteMetadata, route RouteMetadata, openAPIPath string) (OpenAPIInfo, bool) {
	provider := metadata.controller
	if provider == nil || openAPIReflectValueIsNil(provider) {
		return OpenAPIInfo{}, false
	}
	candidates := []string{metadata.fullPath, openAPIPath, route.Path}
	infoByPath := provider.OpenAPI()
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if info, exists := infoByPath[candidate]; exists {
			return info, true
		}
	}
	return OpenAPIInfo{}, false
}

func openAPIReflectValueIsNil(value interface{}) bool {
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func ensureOpenAPIPathParameters(op map[string]interface{}, path string) {
	parameters, _ := op["parameters"].([]interface{})
	existing := make(map[string]struct{}, len(parameters))
	for _, parameter := range parameters {
		definition, ok := parameter.(map[string]interface{})
		if !ok || definition["in"] != "path" {
			continue
		}
		if name, ok := definition["name"].(string); ok {
			existing[name] = struct{}{}
		}
	}
	for _, segment := range strings.Split(path, "/") {
		if len(segment) < 3 || segment[0] != '{' || segment[len(segment)-1] != '}' {
			continue
		}
		name := segment[1 : len(segment)-1]
		if _, ok := existing[name]; ok {
			continue
		}
		parameters = append(parameters, openAPIParameter(name, "path", true, reflect.TypeOf("")))
		existing[name] = struct{}{}
	}
	if len(parameters) > 0 {
		op["parameters"] = parameters
	}
}

func openAPIOperationID(route RouteMetadata, method, path string, seen map[string]RouteMetadata) (string, error) {
	operationID := strings.TrimSpace(route.HandlerName)
	if previous, exists := seen[operationID]; exists {
		if !openAPIGeneratedOperationID(route) {
			return "", fmt.Errorf("duplicate operationId %q for %s %s and %s %s", operationID, previous.Method, previous.Path, route.Method, route.Path)
		}
		operationID += "_" + openAPIRouteOperationSuffix(method, path)
		if previous, exists := seen[operationID]; exists {
			return "", fmt.Errorf("duplicate operationId %q for %s %s and %s %s", operationID, previous.Method, previous.Path, route.Method, route.Path)
		}
	}
	return operationID, nil
}

func openAPIGeneratedOperationID(route RouteMetadata) bool {
	return route.HandlerType != nil &&
		route.HandlerType.Kind() == reflect.Func &&
		strings.TrimSpace(route.HandlerName) == route.HandlerType.String()
}

func openAPIRouteOperationSuffix(method, path string) string {
	replacer := strings.NewReplacer(
		"/", "_",
		"{", "",
		"}", "",
		":", "",
		"*", "",
		"-", "_",
	)
	suffix := strings.Trim(replacer.Replace(strings.ToLower(method)+"_"+path), "_")
	if suffix == "" {
		return "root"
	}
	return suffix
}

func openAPIMethodAllowed(method string) bool {
	switch method {
	case "get", "put", "post", "delete", "options", "head", "patch", "trace":
		return true
	default:
		return false
	}
}

func addStandardOpenAPIErrorResponses(op map[string]interface{}, includeUnauthorized bool) {
	responses, ok := op["responses"].(map[string]interface{})
	if !ok {
		responses = make(map[string]interface{})
		op["responses"] = responses
	}
	responses["400"] = openAPIErrorResponse("Bad Request")
	if includeUnauthorized {
		responses["401"] = openAPIErrorResponse("Unauthorized")
	}
	responses["403"] = openAPIErrorResponse("Forbidden")
	responses["404"] = openAPIErrorResponse("Not Found")
	responses["500"] = openAPIErrorResponse("Internal Server Error")
}

func openAPIErrorResponse(description string) map[string]interface{} {
	return map[string]interface{}{
		"description": description,
		"content": map[string]interface{}{
			"application/json": map[string]interface{}{
				"schema": map[string]interface{}{
					"$ref": "#/components/schemas/ErrorResponse",
				},
			},
		},
	}
}

func openAPIRouteIsPublic(path string, config *SysConfig) bool {
	if config == nil || config.Auth == nil {
		return false
	}
	for _, pattern := range config.Auth.GetPublicPaths() {
		if publicPathMatch(path, pattern) {
			return true
		}
	}
	return false
}

func toOpenAPIPath(path string) string {
	parts := strings.Split(path, "/")
	for i, part := range parts {
		if strings.HasPrefix(part, ":") && len(part) > 1 {
			parts[i] = "{" + part[1:] + "}"
			continue
		}
		if strings.HasPrefix(part, "*") && len(part) > 1 {
			parts[i] = "{" + part[1:] + "}"
		}
	}
	return strings.Join(parts, "/")
}

func enrichOpenAPIOperation(op map[string]interface{}, method string, handlerType reflect.Type, schemas *openAPISchemaBuilder, envelopeMode bool) {
	if handlerType == nil || handlerType.Kind() != reflect.Func {
		return
	}
	parameters := make([]interface{}, 0)
	for i := 0; i < handlerType.NumIn(); i++ {
		argType := derefType(handlerType.In(i))
		if argType == nil || argType == reflect.TypeOf(gin.Context{}) || argType.Kind() != reflect.Struct {
			continue
		}
		parameters = append(parameters, openAPIParametersFromStruct(argType, schemas, make(map[reflect.Type]bool), 0)...)
		if openAPIAllowsJSONRequestBody(method) {
			bodySchema := openAPIRequestBodySchema(argType, schemas, make(map[reflect.Type]bool), 0)
			if bodySchema == nil {
				continue
			}
			op["requestBody"] = map[string]interface{}{
				"required": openAPIRequestBodyRequired(bodySchema),
				"content": map[string]interface{}{
					"application/json": map[string]interface{}{
						"schema": bodySchema,
					},
				},
			}
		}
	}
	if len(parameters) > 0 {
		op["parameters"] = parameters
	}
	if responseSchema := openAPIResponseSchema(handlerType, schemas, envelopeMode); responseSchema != nil {
		op["responses"] = map[string]interface{}{
			"200": map[string]interface{}{
				"description": "OK",
				"content": map[string]interface{}{
					"application/json": map[string]interface{}{
						"schema": responseSchema,
					},
				},
			},
		}
	}
}

func openAPIRequestBodyRequired(schema map[string]interface{}) bool {
	required, ok := schema["required"].([]string)
	return ok && len(required) > 0
}

func openAPIAllowsJSONRequestBody(method string) bool {
	switch strings.ToUpper(strings.TrimSpace(method)) {
	case http.MethodPost, http.MethodPut, http.MethodPatch:
		return true
	default:
		return false
	}
}

func openAPIParametersFromStruct(structType reflect.Type, schemas *openAPISchemaBuilder, visited map[reflect.Type]bool, depth int) []interface{} {
	if depth >= maxOpenAPISchemaDepth || visited[structType] {
		return nil
	}
	visited[structType] = true
	defer delete(visited, structType)
	params := make([]interface{}, 0)
	for i := 0; i < structType.NumField(); i++ {
		field := structType.Field(i)
		if field.PkgPath != "" {
			continue
		}
		fieldType := derefType(field.Type)
		if field.Anonymous && fieldType != nil && fieldType.Kind() == reflect.Struct {
			params = append(params, openAPIParametersFromStruct(fieldType, schemas, visited, depth+1)...)
			continue
		}
		if name := tagFieldName(field.Tag.Get("uri")); name != "" {
			params = append(params, openAPIParameterWithBuilder(name, "path", true, field.Type, schemas))
			continue
		}
		if name := tagFieldName(field.Tag.Get("query")); name != "" {
			params = append(params, openAPIParameterWithBuilder(name, "query", hasRequiredBinding(field), field.Type, schemas))
			continue
		}
		if name := tagFieldName(field.Tag.Get("form")); name != "" {
			params = append(params, openAPIParameterWithBuilder(name, "query", hasRequiredBinding(field), field.Type, schemas))
		}
	}
	return params
}

func openAPIParameter(name, in string, required bool, fieldType reflect.Type) map[string]interface{} {
	return openAPIParameterWithBuilder(name, in, required, fieldType, newOpenAPISchemaBuilder(nil))
}

func openAPIParameterWithBuilder(name, in string, required bool, fieldType reflect.Type, schemas *openAPISchemaBuilder) map[string]interface{} {
	return map[string]interface{}{
		"name":     name,
		"in":       in,
		"required": required,
		"schema":   schemas.schemaForType(fieldType, 0),
	}
}

func openAPIRequestBodySchema(structType reflect.Type, schemas *openAPISchemaBuilder, visited map[reflect.Type]bool, depth int) map[string]interface{} {
	if depth >= maxOpenAPISchemaDepth || visited[structType] {
		return map[string]interface{}{"type": "object"}
	}
	if schemas.isRecursive(structType) {
		return schemas.schemaForType(structType, depth)
	}
	visited[structType] = true
	defer delete(visited, structType)
	properties := make(map[string]interface{})
	required := make([]string, 0)
	for i := 0; i < structType.NumField(); i++ {
		field := structType.Field(i)
		if field.PkgPath != "" || field.Tag.Get("json") == "-" {
			continue
		}
		fieldType := derefType(field.Type)
		if field.Anonymous && fieldType != nil && fieldType.Kind() == reflect.Struct {
			nested := openAPIRequestBodySchema(fieldType, schemas, visited, depth+1)
			if nested != nil {
				if nestedProps, ok := nested["properties"].(map[string]interface{}); ok {
					for key, value := range nestedProps {
						properties[key] = value
					}
				}
				if nestedRequired, ok := nested["required"].([]string); ok {
					required = append(required, nestedRequired...)
				}
			}
			continue
		}
		name := tagFieldName(field.Tag.Get("json"))
		if name == "" {
			name = field.Name
		}
		properties[name] = schemas.schemaForType(field.Type, depth+1)
		if hasRequiredBinding(field) {
			required = append(required, name)
		}
	}
	if len(properties) == 0 {
		return nil
	}
	schema := map[string]interface{}{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func openAPIResponseSchema(handlerType reflect.Type, schemas *openAPISchemaBuilder, envelopeMode bool) map[string]interface{} {
	errorType := reflect.TypeOf((*error)(nil)).Elem()
	for i := 0; i < handlerType.NumOut(); i++ {
		outType := handlerType.Out(i)
		if outType.Implements(errorType) {
			continue
		}
		responseType := reflect.TypeOf(Response{})
		if envelopeMode && derefType(outType) == responseType {
			dataField, _ := responseType.FieldByName("Data")
			return openAPIEnvelopeResponseSchema(schemas.schemaForType(dataField.Type, 0))
		}
		responseSchema := schemas.schemaForType(outType, 0)
		if envelopeMode {
			return openAPIEnvelopeResponseSchema(responseSchema)
		}
		return responseSchema
	}
	return nil
}

func openAPIEnvelopeResponseSchema(dataSchema map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"code":    map[string]interface{}{"type": "integer"},
			"message": map[string]interface{}{"type": "string"},
			"data":    dataSchema,
		},
		"required": []string{"code", "message"},
	}
}

const maxOpenAPISchemaDepth = 64

type openAPISchemaBuilder struct {
	components map[string]interface{}
	names      map[reflect.Type]string
	usedNames  map[string]reflect.Type
}

func newOpenAPISchemaBuilder(components map[string]interface{}) *openAPISchemaBuilder {
	if components == nil {
		components = make(map[string]interface{})
	}
	builder := &openAPISchemaBuilder{
		components: components,
		names:      make(map[reflect.Type]string),
		usedNames:  make(map[string]reflect.Type),
	}
	for name := range components {
		builder.usedNames[name] = nil
	}
	return builder
}

func (b *openAPISchemaBuilder) schemaForType(fieldType reflect.Type, depth int) map[string]interface{} {
	if fieldType == nil {
		return map[string]interface{}{"type": "object"}
	}
	if depth >= maxOpenAPISchemaDepth {
		return map[string]interface{}{"type": "object"}
	}
	fieldType = derefType(fieldType)
	if fieldType == nil {
		return map[string]interface{}{"type": "object"}
	}
	if fieldType == reflect.TypeOf(time.Time{}) {
		return map[string]interface{}{"type": "string", "format": "date-time"}
	}
	switch fieldType.Kind() {
	case reflect.Bool:
		return map[string]interface{}{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]interface{}{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]interface{}{"type": "number"}
	case reflect.String:
		return map[string]interface{}{"type": "string"}
	case reflect.Slice, reflect.Array:
		return map[string]interface{}{
			"type":  "array",
			"items": b.schemaForType(fieldType.Elem(), depth+1),
		}
	case reflect.Map:
		additionalProperties := interface{}(true)
		if fieldType.Elem().Kind() != reflect.Interface {
			additionalProperties = b.schemaForType(fieldType.Elem(), depth+1)
		}
		return map[string]interface{}{
			"type":                 "object",
			"additionalProperties": additionalProperties,
		}
	case reflect.Struct:
		if b.isRecursive(fieldType) {
			return b.componentRef(fieldType, depth)
		}
		return b.structSchema(fieldType, depth)
	case reflect.Interface:
		return map[string]interface{}{"type": "object"}
	default:
		return map[string]interface{}{"type": "string"}
	}
}

func (b *openAPISchemaBuilder) structSchema(structType reflect.Type, depth int) map[string]interface{} {
	properties := make(map[string]interface{})
	required := make([]string, 0)
	for i := 0; i < structType.NumField(); i++ {
		field := structType.Field(i)
		if field.PkgPath != "" || field.Tag.Get("json") == "-" {
			continue
		}
		name := tagFieldName(field.Tag.Get("json"))
		if name == "" {
			name = field.Name
		}
		properties[name] = b.schemaForType(field.Type, depth+1)
		if hasRequiredBinding(field) {
			required = append(required, name)
		}
	}
	schema := map[string]interface{}{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func (b *openAPISchemaBuilder) componentRef(structType reflect.Type, depth int) map[string]interface{} {
	name := b.componentName(structType)
	if _, exists := b.components[name]; !exists {
		b.components[name] = map[string]interface{}{"type": "object"}
		b.components[name] = b.structSchema(structType, depth+1)
	}
	return map[string]interface{}{"$ref": "#/components/schemas/" + name}
}

func (b *openAPISchemaBuilder) componentName(structType reflect.Type) string {
	if name, exists := b.names[structType]; exists {
		return name
	}
	base := structType.Name()
	if base == "" {
		base = "AnonymousObject"
	}
	name := base
	for suffix := 2; ; suffix++ {
		usedBy, exists := b.usedNames[name]
		if !exists || usedBy == structType {
			break
		}
		name = fmt.Sprintf("%s_%d", base, suffix)
	}
	b.names[structType] = name
	b.usedNames[name] = structType
	return name
}

func (b *openAPISchemaBuilder) isRecursive(structType reflect.Type) bool {
	structType = derefType(structType)
	if structType == nil || structType.Kind() != reflect.Struct {
		return false
	}
	for i := 0; i < structType.NumField(); i++ {
		if openAPITypeReaches(fieldElementType(structType.Field(i).Type), structType, make(map[reflect.Type]bool), 0) {
			return true
		}
	}
	return false
}

func openAPITypeReaches(current, target reflect.Type, visited map[reflect.Type]bool, depth int) bool {
	if current == nil || depth >= maxOpenAPISchemaDepth {
		return false
	}
	current = fieldElementType(current)
	if current == target {
		return true
	}
	if current == nil || current.Kind() != reflect.Struct || visited[current] {
		return false
	}
	visited[current] = true
	defer delete(visited, current)
	for i := 0; i < current.NumField(); i++ {
		if openAPITypeReaches(current.Field(i).Type, target, visited, depth+1) {
			return true
		}
	}
	return false
}

func fieldElementType(fieldType reflect.Type) reflect.Type {
	for fieldType != nil {
		switch fieldType.Kind() {
		case reflect.Ptr, reflect.Slice, reflect.Array:
			fieldType = fieldType.Elem()
		case reflect.Map:
			fieldType = fieldType.Elem()
		default:
			return fieldType
		}
	}
	return nil
}

func derefType(t reflect.Type) reflect.Type {
	for t != nil && t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return t
}

func hasRequiredBinding(field reflect.StructField) bool {
	binding := field.Tag.Get("binding")
	return binding == "required" || strings.Contains(binding, "required,") || strings.Contains(binding, ",required")
}

// EnableSwagger 启用 Swagger 文档支持
func (b *Bear) EnableSwagger() *Bear {
	if b == nil {
		return b
	}
	var config *SysConfig
	if b.runtime != nil {
		config = b.runtime.Config
	}
	if isProductionMode(config) {
		return b
	}

	swg := b.Group("/swagger")
	var documentOnce sync.Once
	var document []byte
	var documentErr error

	swg.GET("/doc.json", func(c *gin.Context) {
		documentOnce.Do(func() {
			document, documentErr = b.GenerateOpenAPI()
		})
		if documentErr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": documentErr.Error()})
			return
		}
		c.Header("X-Content-Type-Options", "nosniff")
		c.Data(http.StatusOK, "application/json", document)
	})

	swg.GET("/init.js", func(c *gin.Context) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Data(http.StatusOK, "application/javascript; charset=utf-8", []byte(swaggerUIInitializer))
	})

	swg.GET("", func(c *gin.Context) {
		c.Header("Content-Security-Policy", swaggerUIContentSecurityPolicy)
		c.Header("Referrer-Policy", "no-referrer")
		c.Header("X-Content-Type-Options", "nosniff")
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(swaggerUIHTML))
	})

	return b
}

const swaggerUIContentSecurityPolicy = "default-src 'none'; script-src 'self' https://cdn.jsdelivr.net; style-src 'self' https://cdn.jsdelivr.net; img-src data: https://validator.swagger.io; font-src https://cdn.jsdelivr.net; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'"

const swaggerUIInitializer = `window.addEventListener("load", function () {
  window.ui = SwaggerUIBundle({
    url: "/swagger/doc.json",
    dom_id: "#swagger-ui"
  });
});
`

const swaggerUIHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Swagger UI</title>
  <link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5.17.14/swagger-ui.css" integrity="sha384-wxLW6kwyHktdDGr6Pv1zgm/VGJh99lfUbzSn6HNHBENZlCN7W602k9VkGdxuFvPn" crossorigin="anonymous">
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5.17.14/swagger-ui-bundle.js" integrity="sha384-wmyclcVGX/WhUkdkATwhaK1X1JtiNrr2EoYJ+diV3vj4v6OC5yCeSu+yW13SYJep" crossorigin="anonymous"></script>
  <script src="/swagger/init.js"></script>
</body>
</html>`

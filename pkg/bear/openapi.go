package bear

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"time"

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

// OpenAPISchema 简化的 OpenAPI 3.0 定义结构
type OpenAPISchema struct {
	OpenAPI    string                 `json:"openapi"`
	Info       map[string]interface{} `json:"info"`
	Paths      map[string]interface{} `json:"paths"`
	Components map[string]interface{} `json:"components"`
	Security   []map[string][]string  `json:"security,omitempty"`
}

// GenerateOpenAPI 生成 OpenAPI 3.0 文档内容
func (this *Bear) GenerateOpenAPI() ([]byte, error) {
	config := GetByType[*SysConfig]()
	schema := OpenAPISchema{
		OpenAPI: "3.0.0",
		Info: map[string]interface{}{
			"title":   config.Server.Name,
			"version": "1.0.0",
		},
		Paths: make(map[string]interface{}),
		Components: map[string]interface{}{
			"schemas": map[string]interface{}{
				"ErrorResponse": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"code":    map[string]interface{}{"type": "integer"},
						"message": map[string]interface{}{"type": "string"},
						"data":    map[string]interface{}{"type": "object"},
					},
					"required": []string{"code", "message"},
				},
			},
		},
	}
	if config != nil && config.Auth != nil {
		schema.Components["securitySchemes"] = map[string]interface{}{
			"BearerAuth": map[string]interface{}{
				"type":         "http",
				"scheme":       "bearer",
				"bearerFormat": "JWT",
			},
		}
		schema.Security = []map[string][]string{
			{"BearerAuth": []string{}},
		}
	}

	// 遍历路由元数据
	for _, route := range this.routeRegistry {
		path := route.Path
		if route.GroupName != "" && !strings.HasPrefix(path, "/"+route.GroupName) {
			path = "/" + route.GroupName + path
		}
		// 统一路径格式
		path = strings.ReplaceAll(path, "//", "/")
		publicRoute := openAPIRouteIsPublic(path, config)
		path = toOpenAPIPath(path)

		if _, exists := schema.Paths[path]; !exists {
			schema.Paths[path] = make(map[string]interface{})
		}

		op := map[string]interface{}{
			"operationId": route.HandlerName,
			"responses": map[string]interface{}{
				"200": map[string]interface{}{
					"description": "OK",
				},
			},
		}
		enrichOpenAPIOperation(op, route.HandlerType)
		if publicRoute {
			op["security"] = []map[string][]string{}
		}
		addStandardOpenAPIErrorResponses(op, config != nil && config.Auth != nil && !publicRoute)

		// 检查控制器是否提供了额外元数据
		if bean := GetInjector().Get(route.HandlerType); bean != nil {
			if ctrl, ok := bean.(IOpenAPI); ok {
				if info, exists := ctrl.OpenAPI()[route.Path]; exists {
					op["summary"] = info.Summary
					op["description"] = info.Description
					op["tags"] = info.Tags
				}
			}
		}

		schema.Paths[path].(map[string]interface{})[strings.ToLower(route.Method)] = op
	}

	return json.MarshalIndent(schema, "", "  ")
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
	for _, pattern := range config.Auth.PublicPaths {
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

func enrichOpenAPIOperation(op map[string]interface{}, handlerType reflect.Type) {
	if handlerType == nil || handlerType.Kind() != reflect.Func {
		return
	}
	parameters := make([]interface{}, 0)
	for i := 0; i < handlerType.NumIn(); i++ {
		argType := derefType(handlerType.In(i))
		if argType == reflect.TypeOf(gin.Context{}) || argType.Kind() != reflect.Struct {
			continue
		}
		parameters = append(parameters, openAPIParametersFromStruct(argType)...)
		if bodySchema := openAPIRequestBodySchema(argType); bodySchema != nil {
			op["requestBody"] = map[string]interface{}{
				"required": true,
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
	if responseSchema := openAPIResponseSchema(handlerType); responseSchema != nil {
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

func openAPIParametersFromStruct(structType reflect.Type) []interface{} {
	params := make([]interface{}, 0)
	for i := 0; i < structType.NumField(); i++ {
		field := structType.Field(i)
		if field.PkgPath != "" {
			continue
		}
		fieldType := derefType(field.Type)
		if field.Anonymous && fieldType.Kind() == reflect.Struct {
			params = append(params, openAPIParametersFromStruct(fieldType)...)
			continue
		}
		if name := tagFieldName(field.Tag.Get("uri")); name != "" {
			params = append(params, openAPIParameter(name, "path", true, field.Type))
			continue
		}
		if name := tagFieldName(field.Tag.Get("query")); name != "" {
			params = append(params, openAPIParameter(name, "query", hasRequiredBinding(field), field.Type))
			continue
		}
		if name := tagFieldName(field.Tag.Get("form")); name != "" {
			params = append(params, openAPIParameter(name, "query", hasRequiredBinding(field), field.Type))
		}
	}
	return params
}

func openAPIParameter(name, in string, required bool, fieldType reflect.Type) map[string]interface{} {
	return map[string]interface{}{
		"name":     name,
		"in":       in,
		"required": required,
		"schema":   openAPISchemaForType(fieldType),
	}
}

func openAPIRequestBodySchema(structType reflect.Type) map[string]interface{} {
	properties := make(map[string]interface{})
	required := make([]string, 0)
	for i := 0; i < structType.NumField(); i++ {
		field := structType.Field(i)
		if field.PkgPath != "" {
			continue
		}
		fieldType := derefType(field.Type)
		if field.Anonymous && fieldType.Kind() == reflect.Struct {
			nested := openAPIRequestBodySchema(fieldType)
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
			continue
		}
		properties[name] = openAPISchemaForType(field.Type)
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

func openAPIResponseSchema(handlerType reflect.Type) map[string]interface{} {
	errorType := reflect.TypeOf((*error)(nil)).Elem()
	for i := 0; i < handlerType.NumOut(); i++ {
		outType := handlerType.Out(i)
		if outType.Implements(errorType) {
			continue
		}
		return openAPISchemaForType(outType)
	}
	return nil
}

func openAPISchemaForType(fieldType reflect.Type) map[string]interface{} {
	if fieldType == nil {
		return map[string]interface{}{"type": "object"}
	}
	fieldType = derefType(fieldType)
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
			"items": openAPISchemaForType(fieldType.Elem()),
		}
	case reflect.Map:
		return map[string]interface{}{
			"type":                 "object",
			"additionalProperties": true,
		}
	case reflect.Struct:
		properties := make(map[string]interface{})
		required := make([]string, 0)
		for i := 0; i < fieldType.NumField(); i++ {
			field := fieldType.Field(i)
			if field.PkgPath != "" {
				continue
			}
			name := tagFieldName(field.Tag.Get("json"))
			if name == "" {
				name = field.Name
			}
			properties[name] = openAPISchemaForType(field.Type)
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
	default:
		return map[string]interface{}{"type": "string"}
	}
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
func (this *Bear) EnableSwagger() *Bear {
	swg := this.Group("/swagger")

	// 注册 JSON 文档端点
	swg.GET("/doc.json", func(c *gin.Context) {
		doc, err := this.GenerateOpenAPI()
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.Data(http.StatusOK, "application/json", doc)
	})

	// 简单的 Swagger UI 代理或重定向提示
	swg.GET("", func(c *gin.Context) {
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.String(200, `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <title>Swagger UI</title>
  <link rel="stylesheet" type="text/css" href="https://unpkg.com/swagger-ui-dist@3/swagger-ui.css">
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://unpkg.com/swagger-ui-dist@3/swagger-ui-bundle.js"></script>
  <script>
    window.onload = () => {
      window.ui = SwaggerUIBundle({
        url: '/swagger/doc.json',
        dom_id: '#swagger-ui',
      });
    };
  </script>
</body>
</html>`)
	})

	return this
}

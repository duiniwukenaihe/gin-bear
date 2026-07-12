package bear

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/gin-gonic/gin"
)

type contractController struct{}

type contractRequest struct {
	ID   int64  `uri:"id" binding:"required"`
	Page int    `query:"page"`
	Name string `json:"name" binding:"required"`
}

type contractResponse struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

func legacyOpenAPIHandler() string { return "ok" }

func (c *contractController) Name() string {
	return "ContractController"
}

func (c *contractController) Build(b *Bear) {
	b.Handle(http.MethodPut, "/users/:id", c.Update)
	b.Handle(http.MethodGet, "/public/ping", c.Ping)
}

func (c *contractController) Update(req *contractRequest) (*contractResponse, error) {
	return &contractResponse{ID: req.ID, Name: req.Name}, nil
}

func (c *contractController) Ping() string {
	return "pong"
}

func TestGenerateOpenAPIProducesStrictContract(t *testing.T) {
	resetTestInjector()
	resetGinModeForTest(t)

	cfg := NewSysConfig()
	cfg.DB.Enabled = false
	cfg.Auth.JWTSecret = randomProductionJWTKey(t)
	cfg.Auth.PublicPaths = stringSlicePointer("/api/public/*")
	app := Ignite(cfg)
	app.Attach(NewAuthFairing())
	app.Mount("/api", &contractController{})
	requireNoError(t, app.ApplyAll(context.Background()))

	doc, err := app.GenerateOpenAPI()
	if err != nil {
		t.Fatalf("generate openapi: %v", err)
	}
	validateStrictOpenAPI(t, doc)

	var spec map[string]interface{}
	if err := json.Unmarshal(doc, &spec); err != nil {
		t.Fatalf("decode openapi: %v\n%s", err, string(doc))
	}

	paths := spec["paths"].(map[string]interface{})
	privateOp := paths["/api/users/{id}"].(map[string]interface{})["put"].(map[string]interface{})
	publicOp := paths["/api/public/ping"].(map[string]interface{})["get"].(map[string]interface{})

	if got, ok := privateOp["operationId"].(string); !ok || strings.TrimSpace(got) == "" {
		t.Fatalf("private operationId = %v, want non-empty string", privateOp["operationId"])
	}
	if _, ok := privateOp["security"]; ok {
		t.Fatalf("private operation should inherit global security: %#v", privateOp)
	}
	publicSecurity, ok := publicOp["security"].([]interface{})
	if !ok || len(publicSecurity) != 0 {
		t.Fatalf("public operation security = %#v, want explicit empty override", publicOp["security"])
	}

	requestBody := privateOp["requestBody"].(map[string]interface{})
	content := requestBody["content"].(map[string]interface{})
	if _, ok := content["application/json"]; !ok {
		t.Fatalf("request body missing application/json content: %#v", requestBody)
	}

	for _, status := range []string{"400", "401", "403", "404", "500"} {
		if !openAPIResponseUsesErrorRef(privateOp, status) {
			t.Fatalf("private operation missing %s error response ref: %#v", status, privateOp["responses"])
		}
	}
	for _, status := range []string{"400", "403", "404", "500"} {
		if !openAPIResponseUsesErrorRef(publicOp, status) {
			t.Fatalf("public operation missing %s error response ref: %#v", status, publicOp["responses"])
		}
	}
	if _, ok := publicOp["responses"].(map[string]interface{})["401"]; ok {
		t.Fatalf("public operation should not include 401 response: %#v", publicOp["responses"])
	}
}

func TestGenerateOpenAPIRejectsDuplicateContracts(t *testing.T) {
	handlerType := reflect.TypeOf(func() string { return "" })
	tests := []struct {
		name      string
		routes    []RouteMetadata
		wantError string
	}{
		{
			name: "duplicate operation id",
			routes: []RouteMetadata{
				{Method: http.MethodGet, Path: "/one", HandlerType: handlerType, HandlerName: "duplicate"},
				{Method: http.MethodPost, Path: "/two", HandlerType: handlerType, HandlerName: "duplicate"},
			},
			wantError: "duplicate operationId",
		},
		{
			name: "duplicate method and path",
			routes: []RouteMetadata{
				{Method: http.MethodGet, Path: "/same", HandlerType: handlerType, HandlerName: "one"},
				{Method: http.MethodGet, Path: "/same", HandlerType: handlerType, HandlerName: "two"},
			},
			wantError: "duplicate route",
		},
		{
			name: "missing method",
			routes: []RouteMetadata{
				{Path: "/missing-method", HandlerType: handlerType, HandlerName: "missingMethod"},
			},
			wantError: "method",
		},
		{
			name: "missing path",
			routes: []RouteMetadata{
				{Method: http.MethodGet, HandlerType: handlerType, HandlerName: "missingPath"},
			},
			wantError: "path",
		},
		{
			name: "missing operation id",
			routes: []RouteMetadata{
				{Method: http.MethodGet, Path: "/missing-operation", HandlerType: handlerType},
			},
			wantError: "operationId",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := Ignite(NewSysConfig())
			app.routeRegistry = tt.routes

			_, err := app.GenerateOpenAPI()
			if err == nil {
				t.Fatal("GenerateOpenAPI returned nil error")
			}
			if !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("GenerateOpenAPI error = %v, want substring %q", err, tt.wantError)
			}
		})
	}
}

func TestGenerateOpenAPIAddsPathParametersForOpaqueHandlers(t *testing.T) {
	app := Ignite(NewSysConfig())
	app.routeRegistry = []RouteMetadata{{
		Method:      http.MethodGet,
		Path:        "/opaque/:id",
		HandlerType: reflect.TypeOf(gin.HandlerFunc(func(*gin.Context) {})),
		HandlerName: "legacyOpaqueHandler",
	}}

	doc, err := app.GenerateOpenAPI()
	if err != nil {
		t.Fatalf("GenerateOpenAPI returned error for opaque path route: %v", err)
	}
	validateStrictOpenAPI(t, doc)

	var spec map[string]interface{}
	if err := json.Unmarshal(doc, &spec); err != nil {
		t.Fatalf("decode openapi: %v", err)
	}
	op := spec["paths"].(map[string]interface{})["/opaque/{id}"].(map[string]interface{})["get"].(map[string]interface{})
	parameters, ok := op["parameters"].([]interface{})
	if !ok || !openAPIHasParameter(parameters, "id", "path", "string") {
		t.Fatalf("opaque operation path parameters = %#v, want required string id", op["parameters"])
	}
}

func TestGenerateOpenAPIDoesNotDeclareJSONBodyForGETQueryHandlers(t *testing.T) {
	type queryRequest struct {
		Page     int    `form:"page" json:"page"`
		PageSize int    `form:"page_size" json:"page_size"`
		Keyword  string `form:"keyword" json:"keyword"`
	}
	type queryResponse struct {
		Total int `json:"total"`
	}

	app := Ignite(NewSysConfig())
	app.routeRegistry = []RouteMetadata{{
		Method:      http.MethodGet,
		Path:        "/resources",
		HandlerType: reflect.TypeOf(func(*queryRequest) (*queryResponse, error) { return nil, nil }),
		HandlerName: "queryResources",
	}}

	doc, err := app.GenerateOpenAPI()
	if err != nil {
		t.Fatalf("generate openapi: %v", err)
	}
	var spec map[string]interface{}
	if err := json.Unmarshal(doc, &spec); err != nil {
		t.Fatalf("decode openapi: %v", err)
	}
	op := spec["paths"].(map[string]interface{})["/resources"].(map[string]interface{})["get"].(map[string]interface{})
	if _, found := op["requestBody"]; found {
		t.Fatalf("GET query handler declared request body: %#v", op["requestBody"])
	}
	parameters, ok := op["parameters"].([]interface{})
	if !ok || !openAPIHasParameter(parameters, "page", "query", "integer") {
		t.Fatalf("GET query handler parameters = %#v, want page query parameter", op["parameters"])
	}
}

func TestGenerateOpenAPIRejectsInvalidGeneratedDocument(t *testing.T) {
	type mismatchedPathRequest struct {
		Other string `uri:"other" binding:"required"`
	}
	app := Ignite(NewSysConfig())
	app.routeRegistry = []RouteMetadata{{
		Method:      http.MethodGet,
		Path:        "/opaque/:id",
		HandlerType: reflect.TypeOf(func(*mismatchedPathRequest) {}),
		HandlerName: "invalidOpaqueRoute",
	}}

	_, err := app.GenerateOpenAPI()
	if err == nil {
		t.Fatal("GenerateOpenAPI returned an invalid document")
	}
	if !strings.Contains(err.Error(), "validate generated OpenAPI document") {
		t.Fatalf("GenerateOpenAPI error = %v, want generated document validation error", err)
	}
}

func TestGenerateOpenAPIPreservesLegacyFunctionOperationID(t *testing.T) {
	app := Ignite(NewSysConfig())
	app.Handle(http.MethodGet, "/legacy-operation", legacyOpenAPIHandler)

	doc, err := app.GenerateOpenAPI()
	if err != nil {
		t.Fatalf("GenerateOpenAPI returned error: %v", err)
	}
	var spec map[string]interface{}
	if err := json.Unmarshal(doc, &spec); err != nil {
		t.Fatalf("decode openapi: %v", err)
	}
	op := spec["paths"].(map[string]interface{})["/legacy-operation"].(map[string]interface{})["get"].(map[string]interface{})
	if got, want := op["operationId"], reflect.TypeOf(legacyOpenAPIHandler).String(); got != want {
		t.Fatalf("operationId = %q, want legacy value %q", got, want)
	}
}

func TestGenerateOpenAPIDisambiguatesRepeatedLegacyFunctionOperationIDs(t *testing.T) {
	app := Ignite(NewSysConfig())
	app.Handle(http.MethodGet, "/legacy-one", legacyOpenAPIHandler)
	app.Handle(http.MethodPost, "/legacy-two", legacyOpenAPIHandler)

	doc, err := app.GenerateOpenAPI()
	if err != nil {
		t.Fatalf("GenerateOpenAPI returned error: %v", err)
	}
	var spec map[string]interface{}
	if err := json.Unmarshal(doc, &spec); err != nil {
		t.Fatalf("decode openapi: %v", err)
	}
	paths := spec["paths"].(map[string]interface{})
	first := paths["/legacy-one"].(map[string]interface{})["get"].(map[string]interface{})["operationId"]
	second := paths["/legacy-two"].(map[string]interface{})["post"].(map[string]interface{})["operationId"]
	if first != "func() string" || second == first {
		t.Fatalf("operation IDs = %q and %q, want legacy first value and unique generated duplicate", first, second)
	}
}

func TestGenerateOpenAPIRejectsGeneratedOperationIDCollision(t *testing.T) {
	handlerType := reflect.TypeOf(legacyOpenAPIHandler)
	baseID := handlerType.String()
	app := Ignite(NewSysConfig())
	app.routeRegistry = []RouteMetadata{
		{Method: http.MethodGet, Path: "/explicit", HandlerType: handlerType, HandlerName: baseID + "_get__foo_bar"},
		{Method: http.MethodGet, Path: "/legacy-one", HandlerType: handlerType, HandlerName: baseID},
		{Method: http.MethodGet, Path: "/foo-bar", HandlerType: handlerType, HandlerName: baseID},
	}

	_, err := app.GenerateOpenAPI()
	if err == nil || !strings.Contains(err.Error(), "duplicate operationId") {
		t.Fatalf("GenerateOpenAPI error = %v, want generated operationId collision", err)
	}
}

func validateStrictOpenAPI(t *testing.T, doc []byte) {
	t.Helper()
	loader := openapi3.NewLoader()
	spec, err := loader.LoadFromData(doc)
	if err != nil {
		t.Fatalf("strict OpenAPI parse failed: %v\n%s", err, string(doc))
	}
	if err := spec.Validate(context.Background()); err != nil {
		t.Fatalf("strict OpenAPI validation failed: %v\n%s", err, string(doc))
	}
}

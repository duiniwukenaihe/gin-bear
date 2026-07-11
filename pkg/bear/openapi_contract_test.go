package bear

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
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
	cfg.Auth.JWTSecret = "replace-with-at-least-32-random-characters"
	cfg.Auth.PublicPaths = []string{"/api/public/*"}
	app := Ignite(cfg)
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

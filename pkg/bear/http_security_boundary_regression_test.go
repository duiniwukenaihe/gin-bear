package bear

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

type panickingReadinessNameChecker struct {
	checkCalls atomic.Int32
}

func (*panickingReadinessNameChecker) Name() string {
	panic("postgres://admin:secret@database/internal")
}

func (c *panickingReadinessNameChecker) CheckReady(context.Context) error {
	c.checkCalls.Add(1)
	return nil
}

func TestReadinessCheckerNamePanicBecomesMaskedFailure(t *testing.T) {
	checker := &panickingReadinessNameChecker{}
	results := runReadinessChecks(context.Background(), time.Second, []ReadinessChecker{checker})

	if len(results) != 1 {
		t.Fatalf("results = %#v, want one failure", results)
	}
	if !errors.Is(results[0].Err, errReadinessCheckPanic) {
		t.Fatalf("readiness error = %v, want masked panic failure", results[0].Err)
	}
	if results[0].Name == "" || strings.Contains(results[0].Name, "secret") {
		t.Fatalf("readiness name = %q, want safe fallback", results[0].Name)
	}
	if got := checker.checkCalls.Load(); got != 0 {
		t.Fatalf("CheckReady calls = %d, want 0 after Name failure", got)
	}
}

func TestProductionAndReleaseNilWebSocketConfigUsesSameOriginPolicy(t *testing.T) {
	tests := []struct {
		name string
		env  string
		mode string
	}{
		{name: "production environment", env: "production", mode: gin.DebugMode},
		{name: "release mode", mode: gin.ReleaseMode},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("BEAR_ENV", tt.env)
			t.Setenv("GIN_MODE", "")
			config := NewSysConfig()
			config.Server.Mode = tt.mode
			config.Auth.JWTSecret = "production-websocket-secret-with-32-characters"
			config.WS = nil

			if err := validateProductionSecurity(config); err != nil {
				t.Fatalf("validateProductionSecurity() error = %v, want unused nil WebSocket config allowed", err)
			}

			for _, origin := range []string{"", "http://app.example.com", "https://app.example.com"} {
				request := httptest.NewRequest(http.MethodGet, "http://app.example.com/ws", nil)
				request.Header.Set("Origin", origin)
				if !websocketOriginAllowed(config, request) {
					t.Fatalf("websocketOriginAllowed() denied same-origin %q", origin)
				}
			}

			evil := httptest.NewRequest(http.MethodGet, "http://app.example.com/ws", nil)
			evil.Header.Set("Origin", "https://evil.example.com")
			if websocketOriginAllowed(config, evil) {
				t.Fatal("websocketOriginAllowed() accepted cross-origin request with nil production WebSocket config")
			}
		})
	}
}

func TestStatusResponseRejectsErrorStatusesThroughSanitizedErrorPath(t *testing.T) {
	const secret = "internal-token-that-must-not-leak"
	for _, status := range []int{http.StatusBadRequest, http.StatusNotFound, http.StatusInternalServerError, 599} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			response := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(response)
			ctx.Request = httptest.NewRequest(http.MethodGet, "/value", nil)

			writeSuccessWithConfig(ctx, nil, WithStatus(status, map[string]string{"secret": secret}))

			if response.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
			}
			var body Response
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode response: %v\n%s", err, response.Body.String())
			}
			if body.Code != http.StatusInternalServerError || body.Message != "Internal server error" || body.Data != nil {
				t.Fatalf("body = %#v, want sanitized internal error", body)
			}
			if strings.Contains(response.Body.String(), secret) {
				t.Fatalf("response leaked successful payload: %s", response.Body.String())
			}
		})
	}
}

type openAPIEnvelopePayload struct {
	ID int64 `json:"id"`
}

func TestEnvelopeOpenAPIResponseSchemaMatchesRuntimeEnvelope(t *testing.T) {
	config := NewSysConfig()
	if err := config.SetResponseMode("envelope"); err != nil {
		t.Fatal(err)
	}
	app := &Bear{
		runtime: &Runtime{Config: config},
		routeRegistry: []RouteMetadata{
			{
				Method:      http.MethodGet,
				Path:        "/payload",
				HandlerType: reflect.TypeOf(func() openAPIEnvelopePayload { return openAPIEnvelopePayload{} }),
				HandlerName: "envelopePayload",
			},
			{
				Method:      http.MethodGet,
				Path:        "/response",
				HandlerType: reflect.TypeOf(func() Response { return Response{} }),
				HandlerName: "explicitResponse",
			},
		},
	}

	document, err := app.GenerateOpenAPI()
	if err != nil {
		t.Fatalf("GenerateOpenAPI() error = %v", err)
	}
	var spec map[string]interface{}
	if err := json.Unmarshal(document, &spec); err != nil {
		t.Fatalf("decode OpenAPI: %v", err)
	}

	payloadSchema := openAPISuccessSchemaForTest(t, spec, "/payload")
	payloadProperties := requireOpenAPIEnvelopeProperties(t, payloadSchema)
	dataSchema, ok := payloadProperties["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("payload data schema = %#v", payloadProperties["data"])
	}
	dataProperties, _ := dataSchema["properties"].(map[string]interface{})
	if _, ok := dataProperties["id"]; !ok {
		t.Fatalf("payload data properties = %#v, want id", dataProperties)
	}

	responseSchema := openAPISuccessSchemaForTest(t, spec, "/response")
	responseProperties := requireOpenAPIEnvelopeProperties(t, responseSchema)
	responseData, ok := responseProperties["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("Response data schema = %#v", responseProperties["data"])
	}
	if nested, _ := responseData["properties"].(map[string]interface{}); nested["code"] != nil || nested["message"] != nil {
		t.Fatalf("Response schema was wrapped twice: %#v", responseSchema)
	}
}

func TestOpenAPIRequestBodyFallsBackToGoFieldNames(t *testing.T) {
	type request struct {
		Untagged string
		Optional string `json:",omitempty"`
		Named    string `json:"named,omitempty"`
		Hidden   string `json:"-"`
	}
	app := &Bear{routeRegistry: []RouteMetadata{{
		Method:      http.MethodPost,
		Path:        "/requests",
		HandlerType: reflect.TypeOf(func(*request) string { return "" }),
		HandlerName: "createRequest",
	}}}

	document, err := app.GenerateOpenAPI()
	if err != nil {
		t.Fatalf("GenerateOpenAPI() error = %v", err)
	}
	var spec map[string]interface{}
	if err := json.Unmarshal(document, &spec); err != nil {
		t.Fatalf("decode OpenAPI: %v", err)
	}
	operation := spec["paths"].(map[string]interface{})["/requests"].(map[string]interface{})["post"].(map[string]interface{})
	requestBody := operation["requestBody"].(map[string]interface{})
	content := requestBody["content"].(map[string]interface{})["application/json"].(map[string]interface{})
	schema := content["schema"].(map[string]interface{})
	properties := schema["properties"].(map[string]interface{})

	for _, name := range []string{"Untagged", "Optional", "named"} {
		if _, ok := properties[name]; !ok {
			t.Fatalf("request properties = %#v, want %q", properties, name)
		}
	}
	if _, ok := properties["Hidden"]; ok {
		t.Fatalf("request properties include json:- field: %#v", properties)
	}
}

func openAPISuccessSchemaForTest(t *testing.T, spec map[string]interface{}, path string) map[string]interface{} {
	t.Helper()
	operation := spec["paths"].(map[string]interface{})[path].(map[string]interface{})["get"].(map[string]interface{})
	response := operation["responses"].(map[string]interface{})["200"].(map[string]interface{})
	content := response["content"].(map[string]interface{})["application/json"].(map[string]interface{})
	return content["schema"].(map[string]interface{})
}

func requireOpenAPIEnvelopeProperties(t *testing.T, schema map[string]interface{}) map[string]interface{} {
	t.Helper()
	properties, ok := schema["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("response schema = %#v, want object properties", schema)
	}
	for _, name := range []string{"code", "message", "data"} {
		if _, ok := properties[name]; !ok {
			t.Fatalf("response properties = %#v, want %q", properties, name)
		}
	}
	return properties
}

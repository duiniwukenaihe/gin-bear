package bear

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/golang-jwt/jwt/v5"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"
	"gorm.io/gorm"
)

func resetTestInjector() {
	injector = &BeanFactory{
		beans: make(map[reflect.Type]any),
	}
	handlerCache = sync.Map{}
}

func resetGinModeForTest(t *testing.T) {
	t.Helper()
	gin.SetMode(gin.DebugMode)
	t.Cleanup(func() {
		gin.SetMode(gin.DebugMode)
	})
}

func TestIgniteAllowsDatabaseDisabledWithoutDSN(t *testing.T) {
	resetTestInjector()
	cfg := NewSysConfig()
	cfg.DB.Enabled = false
	cfg.DB.DSN = ""
	cfg.DB.DBName = ""

	app := Ignite(cfg)

	if app == nil {
		t.Fatal("expected Ignite to return an app")
	}
}

func TestIgniteRegistersProvidedConfigBeforeMiddleware(t *testing.T) {
	resetTestInjector()
	cfg := NewSysConfig()
	cfg.DB.Enabled = false
	cfg.Middleware.PerformanceLogLevel = "debug"
	cfg.Middleware.SlowRequestThreshold = "250ms"

	app := Ignite(cfg)

	if got := GetByType[*SysConfig](); got != cfg {
		t.Fatal("expected provided config to be registered")
	}
	if len(app.Handlers) == 0 {
		t.Fatal("expected base middleware to be registered")
	}
}

func TestSysConfigValidateRejectsSemanticErrors(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*SysConfig)
		wantErr string
	}{
		{
			name: "unsupported tracing exporter",
			mutate: func(cfg *SysConfig) {
				cfg.Tracing.Enabled = true
				cfg.Tracing.Exporter = "zipkin"
			},
			wantErr: "tracing.exporter",
		},
		{
			name: "invalid tracing sample rate",
			mutate: func(cfg *SysConfig) {
				cfg.Tracing.SampleRate = 1.5
			},
			wantErr: "tracing.sample_rate",
		},
		{
			name: "metrics path must be absolute",
			mutate: func(cfg *SysConfig) {
				cfg.Metrics.Path = "metrics"
			},
			wantErr: "metrics.path",
		},
		{
			name: "server timeout must parse",
			mutate: func(cfg *SysConfig) {
				cfg.Server.ReadTimeout = "soon"
			},
			wantErr: "server.read_timeout",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := NewSysConfig()
			tt.mutate(cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatalf("expected validation error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validation error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}

func TestBuildHTTPServerAppliesConfiguredTimeouts(t *testing.T) {
	resetTestInjector()
	cfg := NewSysConfig()
	cfg.DB.Enabled = false
	cfg.Server.Port = 9099
	cfg.Server.ReadHeaderTimeout = "2s"
	cfg.Server.ReadTimeout = "3s"
	cfg.Server.WriteTimeout = "4s"
	cfg.Server.IdleTimeout = "5s"
	cfg.Server.MaxHeaderBytes = 8192

	app := Ignite(cfg)
	srv := app.buildHTTPServer(cfg)

	if srv.Addr != ":9099" {
		t.Fatalf("addr = %q", srv.Addr)
	}
	if srv.ReadHeaderTimeout != 2*time.Second {
		t.Fatalf("ReadHeaderTimeout = %s", srv.ReadHeaderTimeout)
	}
	if srv.ReadTimeout != 3*time.Second {
		t.Fatalf("ReadTimeout = %s", srv.ReadTimeout)
	}
	if srv.WriteTimeout != 4*time.Second {
		t.Fatalf("WriteTimeout = %s", srv.WriteTimeout)
	}
	if srv.IdleTimeout != 5*time.Second {
		t.Fatalf("IdleTimeout = %s", srv.IdleTimeout)
	}
	if srv.MaxHeaderBytes != 8192 {
		t.Fatalf("MaxHeaderBytes = %d", srv.MaxHeaderBytes)
	}
}

func TestCORSMiddlewareUsesConfiguredOrigin(t *testing.T) {
	resetTestInjector()
	cfg := NewSysConfig()
	cfg.CORS.Enabled = true
	cfg.CORS.AllowOrigins = []string{"https://example.com"}
	cfg.CORS.AllowCredentials = true
	GetInjector().Set(cfg)

	router := gin.New()
	router.Use(CORSMiddleware())
	router.GET("/ping", func(c *gin.Context) { c.String(http.StatusOK, "pong") })

	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	req.Header.Set("Origin", "https://example.com")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://example.com" {
		t.Fatalf("origin header = %q", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Fatalf("credentials header = %q", got)
	}
}

func TestHandleErrorHidesUnexpectedErrorDetails(t *testing.T) {
	resetTestInjector()
	cfg := NewSysConfig()
	cfg.DB.Enabled = false
	app := Ignite(cfg)
	app.GET("/boom", Convert(func() (string, error) {
		return "", errors.New("sql: password=secret")
	}))

	req := httptest.NewRequest(http.MethodGet, "/boom", nil)
	w := httptest.NewRecorder()
	app.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", w.Code)
	}
	if strings.Contains(w.Body.String(), "password=secret") {
		t.Fatalf("response leaked internal error: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Internal server error") {
		t.Fatalf("response missing safe error message: %s", w.Body.String())
	}
}

func TestBindingErrorUsesStableClientMessage(t *testing.T) {
	resetTestInjector()
	cfg := NewSysConfig()
	cfg.DB.Enabled = false
	app := Ignite(cfg)
	type createReq struct {
		Name string `json:"name" binding:"required"`
	}
	app.POST("/users", Convert(func(req *createReq) string {
		return req.Name
	}))

	req := httptest.NewRequest(http.MethodPost, "/users", bytes.NewBufferString(`{}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	app.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", w.Code)
	}
	if strings.Contains(w.Body.String(), "Key:") {
		t.Fatalf("response leaked validator internals: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Invalid request") {
		t.Fatalf("response missing stable validation message: %s", w.Body.String())
	}
}

func TestPathIntParameterRejectsInvalidValue(t *testing.T) {
	resetTestInjector()
	resetGinModeForTest(t)
	cfg := NewSysConfig()
	cfg.DB.Enabled = false
	app := Ignite(cfg)
	app.Mount("", &pathParamTestController{})
	app.ApplyAll(context.Background())

	req := httptest.NewRequest(http.MethodGet, "/users/not-a-number", nil)
	w := httptest.NewRecorder()
	app.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Invalid path parameter") {
		t.Fatalf("response missing path parameter error: %s", w.Body.String())
	}
}

func TestIgniteConfiguresGinReleaseMode(t *testing.T) {
	resetTestInjector()
	resetGinModeForTest(t)
	cfg := NewSysConfig()
	cfg.DB.Enabled = false
	cfg.Server.Mode = "release"
	cfg.Auth.JWTSecret = "replace-with-at-least-32-random-characters"

	Ignite(cfg)

	if got := gin.Mode(); got != gin.ReleaseMode {
		t.Fatalf("gin mode = %q", got)
	}
}

func TestIgniteRejectsWeakJWTSecretInProduction(t *testing.T) {
	resetTestInjector()
	resetGinModeForTest(t)
	cfg := NewSysConfig()
	cfg.DB.Enabled = false
	cfg.Server.Mode = "release"
	cfg.Auth.JWTSecret = "bear-secret"

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected weak JWT secret panic")
		}
		if !strings.Contains(r.(string), "weak jwt secret") {
			t.Fatalf("unexpected panic: %v", r)
		}
	}()

	Ignite(cfg)
}

func TestIgniteUsesBearEnvProductionMode(t *testing.T) {
	resetTestInjector()
	resetGinModeForTest(t)
	t.Setenv("BEAR_ENV", "production")
	t.Setenv("GIN_MODE", "")
	cfg := NewSysConfig()
	cfg.DB.Enabled = false
	cfg.Auth.JWTSecret = "replace-with-at-least-32-random-characters"

	Ignite(cfg)

	if got := gin.Mode(); got != gin.ReleaseMode {
		t.Fatalf("gin mode = %q", got)
	}
}

func TestIgniteAppliesTrustedProxies(t *testing.T) {
	resetTestInjector()
	resetGinModeForTest(t)
	cfg := NewSysConfig()
	cfg.DB.Enabled = false
	cfg.Server.TrustedProxies = []string{"127.0.0.1"}

	app := Ignite(cfg)
	app.GET("/ip", func(c *gin.Context) {
		c.String(http.StatusOK, c.ClientIP())
	})

	req := httptest.NewRequest(http.MethodGet, "/ip", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	w := httptest.NewRecorder()

	app.ServeHTTP(w, req)

	if got := w.Body.String(); got != "203.0.113.9" {
		t.Fatalf("client ip = %q", got)
	}
}

type testReadyChecker struct {
	name string
	err  error
}

func (c *testReadyChecker) Name() string {
	return c.name
}

func (c *testReadyChecker) CheckReady(ctx context.Context) error {
	return c.err
}

type authTestController struct{}

func (c *authTestController) Name() string {
	return "AuthTestController"
}

func (c *authTestController) Build(b *Bear) {
	b.Handle("GET", "/public/ping", func() string { return "pong" })
	b.Handle("GET", "/private/ping", func() string { return "secret" })
}

type pathParamTestController struct{}

func (c *pathParamTestController) Name() string {
	return "PathParamTestController"
}

func (c *pathParamTestController) Build(b *Bear) {
	b.Handle("GET", "/users/:id", func(id int64) string {
		return "user"
	})
}

type openAPITestController struct{}
type openAPIPublicTestController struct{}

type openAPITestRequest struct {
	ID   int64  `uri:"id" binding:"required"`
	Page int    `form:"page"`
	Name string `json:"name" binding:"required"`
}

type openAPITestResponse struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

func (c *openAPITestController) Name() string {
	return "OpenAPITestController"
}

func (c *openAPITestController) Build(b *Bear) {
	b.Handle(http.MethodPut, "/users/:id", c.Update)
}

func (c *openAPITestController) Update(req *openAPITestRequest) (*openAPITestResponse, error) {
	return &openAPITestResponse{ID: req.ID, Name: req.Name}, nil
}

func (c *openAPIPublicTestController) Name() string {
	return "OpenAPIPublicTestController"
}

func (c *openAPIPublicTestController) Build(b *Bear) {
	b.Handle(http.MethodGet, "/public/ping", c.Ping)
	b.Handle(http.MethodGet, "/private/ping", c.Ping)
}

func (c *openAPIPublicTestController) Ping() string {
	return "pong"
}

func TestHealthEndpointsExposeLiveAndReady(t *testing.T) {
	resetTestInjector()
	resetGinModeForTest(t)
	cfg := NewSysConfig()
	cfg.DB.Enabled = false

	app := Ignite(cfg)
	app.EnableHealth()
	app.ApplyAll(context.Background())

	for _, path := range []string{"/health", "/live", "/ready", "/metrics"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		app.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s status = %d body = %s", path, w.Code, w.Body.String())
		}
	}
}

func TestVersionEndpointExposesBuildInfo(t *testing.T) {
	resetTestInjector()
	resetGinModeForTest(t)
	oldVersion, oldCommit, oldBuildTime := Version, Commit, BuildTime
	Version = "v1.2.3"
	Commit = "abc123"
	BuildTime = "2026-06-19T00:00:00Z"
	t.Cleanup(func() {
		Version, Commit, BuildTime = oldVersion, oldCommit, oldBuildTime
	})

	cfg := NewSysConfig()
	cfg.DB.Enabled = false
	app := Ignite(cfg)
	app.EnableHealth()
	app.ApplyAll(context.Background())

	req := httptest.NewRequest(http.MethodGet, "/version", nil)
	w := httptest.NewRecorder()
	app.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	var got map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode version response: %v", err)
	}
	if got["version"] != "v1.2.3" || got["commit"] != "abc123" || got["build_time"] != "2026-06-19T00:00:00Z" {
		t.Fatalf("unexpected build info: %#v", got)
	}
	if got["go_version"] == "" || got["os"] == "" || got["arch"] == "" {
		t.Fatalf("missing runtime fields: %#v", got)
	}
}

func TestMetricsEndpointExportsHTTPRequestMetrics(t *testing.T) {
	resetTestInjector()
	resetGinModeForTest(t)
	resetMetricsForTest()
	cfg := NewSysConfig()
	cfg.DB.Enabled = false
	cfg.Metrics.Enabled = true
	cfg.Metrics.Path = "/metrics"

	app := Ignite(cfg)
	app.EnableMetrics()
	app.GET("/ok", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})
	app.GET("/bad", func(c *gin.Context) {
		c.String(http.StatusInternalServerError, "bad")
	})

	for _, path := range []string{"/ok", "/bad"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		app.ServeHTTP(w, req)
	}

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	app.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{
		`# HELP gin_bear_http_requests_total Total HTTP requests handled by gin-bear.`,
		`gin_bear_http_requests_total{method="GET",route="/ok",status="200"} 1`,
		`gin_bear_http_requests_total{method="GET",route="/bad",status="500"} 1`,
		`gin_bear_http_errors_total{method="GET",route="/bad",status="500"} 1`,
		`gin_bear_http_request_duration_seconds_bucket{method="GET",route="/ok",status="200",le="0.005"}`,
		`gin_bear_http_request_duration_seconds_sum{method="GET",route="/ok",status="200"}`,
		`gin_bear_http_request_duration_seconds_count{method="GET",route="/ok",status="200"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics output missing %q:\n%s", want, body)
		}
	}
}

func TestTracingMiddlewareCreatesServerSpanAndExtractsTraceparent(t *testing.T) {
	resetTestInjector()
	resetGinModeForTest(t)
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() {
		_ = provider.Shutdown(context.Background())
	})

	router := gin.New()
	router.Use(TracingMiddleware(provider, propagation.TraceContext{}))
	router.GET("/users/:id", func(c *gin.Context) {
		spanContext := oteltrace.SpanContextFromContext(c.Request.Context())
		if !spanContext.IsValid() {
			t.Fatal("expected handler context to contain a valid span")
		}
		c.String(http.StatusAccepted, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/users/42?debug=true", nil)
	req.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	req.Header.Set("X-Request-ID", "rid-123")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("span count = %d, spans = %#v", len(spans), spans)
	}
	span := spans[0]
	if span.Name != "GET /users/:id" {
		t.Fatalf("span name = %q", span.Name)
	}
	if span.SpanKind != oteltrace.SpanKindServer {
		t.Fatalf("span kind = %s", span.SpanKind)
	}
	if got := span.Parent.TraceID().String(); got != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("parent trace id = %s", got)
	}
	if !spanHasStringAttr(span.Attributes, "http.request.method", "GET") {
		t.Fatalf("missing method attr: %#v", span.Attributes)
	}
	if !spanHasStringAttr(span.Attributes, "http.route", "/users/:id") {
		t.Fatalf("missing route attr: %#v", span.Attributes)
	}
	if !spanHasIntAttr(span.Attributes, "http.response.status_code", 202) {
		t.Fatalf("missing status attr: %#v", span.Attributes)
	}
	if !spanHasStringAttr(span.Attributes, "gin_bear.request_id", "rid-123") {
		t.Fatalf("missing request id attr: %#v", span.Attributes)
	}
}

func TestEnableTracingRegistersMiddlewareOnce(t *testing.T) {
	resetTestInjector()
	resetGinModeForTest(t)
	cfg := NewSysConfig()
	cfg.DB.Enabled = false
	cfg.Tracing.Enabled = true
	cfg.Tracing.Exporter = "none"
	cfg.Tracing.ServiceName = "trace-test"

	app := Ignite(cfg)
	before := len(app.Handlers)
	app.EnableTracing(context.Background())
	afterFirst := len(app.Handlers)
	app.EnableTracing(context.Background())
	afterSecond := len(app.Handlers)

	if afterFirst != before+1 {
		t.Fatalf("handler count after EnableTracing = %d, want %d", afterFirst, before+1)
	}
	if afterSecond != afterFirst {
		t.Fatalf("EnableTracing should be idempotent, handlers after second call = %d, first = %d", afterSecond, afterFirst)
	}
}

func TestGenerateOpenAPIIncludesParametersRequestBodyAndResponseSchema(t *testing.T) {
	resetTestInjector()
	resetGinModeForTest(t)
	cfg := NewSysConfig()
	cfg.DB.Enabled = false
	cfg.Server.Name = "openapi-test"

	app := Ignite(cfg)
	app.Mount("/api", &openAPITestController{})
	if err := app.ApplyAll(context.Background()); err != nil {
		t.Fatalf("apply all: %v", err)
	}

	doc, err := app.GenerateOpenAPI()
	if err != nil {
		t.Fatalf("generate openapi: %v", err)
	}
	var spec map[string]interface{}
	if err := json.Unmarshal(doc, &spec); err != nil {
		t.Fatalf("decode openapi: %v\n%s", err, string(doc))
	}

	paths := spec["paths"].(map[string]interface{})
	pathItem, ok := paths["/api/users/{id}"].(map[string]interface{})
	if !ok {
		t.Fatalf("missing openapi path /api/users/{id}: %s", string(doc))
	}
	op := pathItem["put"].(map[string]interface{})

	parameters := op["parameters"].([]interface{})
	if !openAPIHasParameter(parameters, "id", "path", "integer") {
		t.Fatalf("missing path id integer parameter: %#v", parameters)
	}
	if !openAPIHasParameter(parameters, "page", "query", "integer") {
		t.Fatalf("missing query page integer parameter: %#v", parameters)
	}

	requestBody := op["requestBody"].(map[string]interface{})
	content := requestBody["content"].(map[string]interface{})
	jsonContent := content["application/json"].(map[string]interface{})
	bodySchema := jsonContent["schema"].(map[string]interface{})
	bodyProps := bodySchema["properties"].(map[string]interface{})
	if bodyProps["name"].(map[string]interface{})["type"] != "string" {
		t.Fatalf("missing request body name string schema: %#v", bodySchema)
	}

	responses := op["responses"].(map[string]interface{})
	okResponse := responses["200"].(map[string]interface{})
	responseContent := okResponse["content"].(map[string]interface{})
	responseJSON := responseContent["application/json"].(map[string]interface{})
	responseSchema := responseJSON["schema"].(map[string]interface{})
	responseProps := responseSchema["properties"].(map[string]interface{})
	if responseProps["id"].(map[string]interface{})["type"] != "integer" {
		t.Fatalf("missing response id integer schema: %#v", responseSchema)
	}
}

func TestGenerateOpenAPIIncludesJWTSecurityScheme(t *testing.T) {
	resetTestInjector()
	resetGinModeForTest(t)
	cfg := NewSysConfig()
	cfg.DB.Enabled = false
	cfg.Server.Name = "secure-openapi-test"
	cfg.Auth.JWTSecret = "replace-with-at-least-32-random-characters"

	app := Ignite(cfg)
	app.Mount("/api", &openAPITestController{})
	if err := app.ApplyAll(context.Background()); err != nil {
		t.Fatalf("apply all: %v", err)
	}

	doc, err := app.GenerateOpenAPI()
	if err != nil {
		t.Fatalf("generate openapi: %v", err)
	}
	var spec map[string]interface{}
	if err := json.Unmarshal(doc, &spec); err != nil {
		t.Fatalf("decode openapi: %v\n%s", err, string(doc))
	}

	components := spec["components"].(map[string]interface{})
	securitySchemes := components["securitySchemes"].(map[string]interface{})
	bearerAuth := securitySchemes["BearerAuth"].(map[string]interface{})
	if bearerAuth["type"] != "http" || bearerAuth["scheme"] != "bearer" || bearerAuth["bearerFormat"] != "JWT" {
		t.Fatalf("unexpected bearer auth scheme: %#v", bearerAuth)
	}
	security := spec["security"].([]interface{})
	if len(security) != 1 {
		t.Fatalf("security length = %d: %#v", len(security), security)
	}
	requirement := security[0].(map[string]interface{})
	if _, ok := requirement["BearerAuth"]; !ok {
		t.Fatalf("missing BearerAuth requirement: %#v", requirement)
	}
}

func TestGenerateOpenAPIMarksPublicPathsWithoutSecurity(t *testing.T) {
	resetTestInjector()
	resetGinModeForTest(t)
	cfg := NewSysConfig()
	cfg.DB.Enabled = false
	cfg.Auth.PublicPaths = []string{"/api/public/*"}

	app := Ignite(cfg)
	app.Mount("/api", &openAPIPublicTestController{})
	if err := app.ApplyAll(context.Background()); err != nil {
		t.Fatalf("apply all: %v", err)
	}

	doc, err := app.GenerateOpenAPI()
	if err != nil {
		t.Fatalf("generate openapi: %v", err)
	}
	var spec map[string]interface{}
	if err := json.Unmarshal(doc, &spec); err != nil {
		t.Fatalf("decode openapi: %v\n%s", err, string(doc))
	}

	paths := spec["paths"].(map[string]interface{})
	publicOp := paths["/api/public/ping"].(map[string]interface{})["get"].(map[string]interface{})
	security, ok := publicOp["security"].([]interface{})
	if !ok {
		t.Fatalf("public operation should override security: %#v", publicOp)
	}
	if len(security) != 0 {
		t.Fatalf("public operation security = %#v, want empty override", security)
	}
	privateOp := paths["/api/private/ping"].(map[string]interface{})["get"].(map[string]interface{})
	if _, ok := privateOp["security"]; ok {
		t.Fatalf("private operation should inherit global security: %#v", privateOp)
	}
}

func openAPIHasParameter(parameters []interface{}, name, in, typ string) bool {
	for _, raw := range parameters {
		param := raw.(map[string]interface{})
		if param["name"] != name || param["in"] != in {
			continue
		}
		schema := param["schema"].(map[string]interface{})
		return schema["type"] == typ
	}
	return false
}

func spanHasStringAttr(attrs []attribute.KeyValue, key string, want string) bool {
	for _, attr := range attrs {
		if string(attr.Key) == key && attr.Value.AsString() == want {
			return true
		}
	}
	return false
}

func spanHasIntAttr(attrs []attribute.KeyValue, key string, want int64) bool {
	for _, attr := range attrs {
		if string(attr.Key) == key && attr.Value.AsInt64() == want {
			return true
		}
	}
	return false
}

func TestReadyEndpointFailsWhenDependencyIsNotReady(t *testing.T) {
	resetTestInjector()
	resetGinModeForTest(t)
	cfg := NewSysConfig()
	cfg.DB.Enabled = false

	app := Ignite(cfg)
	app.Beans(&testReadyChecker{name: "broken", err: errors.New("not connected")})
	app.EnableHealth()
	app.ApplyAll(context.Background())

	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	w := httptest.NewRecorder()
	app.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "broken") {
		t.Fatalf("response missing failing dependency: %s", w.Body.String())
	}
}

func TestAuthFairingUsesConfiguredPublicPaths(t *testing.T) {
	resetTestInjector()
	resetGinModeForTest(t)
	cfg := NewSysConfig()
	cfg.DB.Enabled = false
	cfg.Auth.PublicPaths = []string{"/public/*"}

	app := Ignite(cfg)
	app.Attach(NewAuthFairing())
	app.Mount("", &authTestController{})
	app.ApplyAll(context.Background())

	publicReq := httptest.NewRequest(http.MethodGet, "/public/ping", nil)
	publicW := httptest.NewRecorder()
	app.ServeHTTP(publicW, publicReq)
	if publicW.Code != http.StatusOK {
		t.Fatalf("public status = %d body = %s", publicW.Code, publicW.Body.String())
	}

	privateReq := httptest.NewRequest(http.MethodGet, "/private/ping", nil)
	privateW := httptest.NewRecorder()
	app.ServeHTTP(privateW, privateReq)
	if privateW.Code != http.StatusBadRequest {
		t.Fatalf("private status = %d body = %s", privateW.Code, privateW.Body.String())
	}
}

func TestJWTUtilRejectsUnexpectedSigningMethod(t *testing.T) {
	util := NewJWTUtil("replace-with-at-least-32-random-characters", 24)
	token := jwt.NewWithClaims(jwt.SigningMethodHS512, CustomClaims{
		UserID: 1,
		Email:  "a@example.com",
	})
	tokenStr, err := token.SignedString([]byte(util.Config.Secret))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}

	if _, err := util.ParseToken(tokenStr); err == nil {
		t.Fatal("expected signing method rejection")
	}
}

func TestAuthTokenBlacklistKeyUsesTokenHash(t *testing.T) {
	manager := NewAuthTokenManager()
	token := "header.payload.signature"

	key := manager.blacklistKey(token)

	if strings.Contains(key, token) {
		t.Fatalf("blacklist key leaked token: %s", key)
	}
	if !strings.HasPrefix(key, "bear:auth:blacklist:") {
		t.Fatalf("unexpected key prefix: %s", key)
	}
}

func TestConvertBindsURIQueryAndJSONTogether(t *testing.T) {
	resetTestInjector()
	resetGinModeForTest(t)
	cfg := NewSysConfig()
	cfg.DB.Enabled = false
	app := Ignite(cfg)

	type mixedReq struct {
		ID   int64  `uri:"id" binding:"required"`
		Page int    `form:"page"`
		Name string `json:"name" binding:"required"`
	}
	app.PUT("/users/:id", Convert(func(req *mixedReq) map[string]interface{} {
		return map[string]interface{}{
			"id":   req.ID,
			"page": req.Page,
			"name": req.Name,
		}
	}))

	req := httptest.NewRequest(http.MethodPut, "/users/42?page=3", bytes.NewBufferString(`{"name":"alice"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	app.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	var got map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got["id"] != float64(42) || got["page"] != float64(3) || got["name"] != "alice" {
		t.Fatalf("unexpected bound request: %#v", got)
	}
}

func TestBearHandleRegistersOnRootWithoutApplyAll(t *testing.T) {
	resetTestInjector()
	resetGinModeForTest(t)
	cfg := NewSysConfig()
	cfg.DB.Enabled = false
	app := Ignite(cfg)

	app.Handle(http.MethodGet, "/direct", func() string { return "ok" })

	req := httptest.NewRequest(http.MethodGet, "/direct", nil)
	w := httptest.NewRecorder()
	app.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "ok") {
		t.Fatalf("response missing handler result: %s", w.Body.String())
	}
}

func TestRedisRateLimiterCanFailClosedWhenRedisUnavailable(t *testing.T) {
	adapter := &RedisAdapter{Client: redis.NewClient(&redis.Options{
		Addr:        "127.0.0.1:1",
		DialTimeout: time.Millisecond,
		ReadTimeout: time.Millisecond,
	})}
	defer adapter.Client.Close()
	limiter := NewRedisRateLimiter(adapter, 1, time.Second)
	limiter.FailClosed = true

	if limiter.Allow(context.Background(), "client") {
		t.Fatal("expected unavailable redis to deny when fail closed")
	}
}

func TestRedisRateLimiterDefaultsToFailOpenWhenRedisUnavailable(t *testing.T) {
	adapter := &RedisAdapter{Client: redis.NewClient(&redis.Options{
		Addr:        "127.0.0.1:1",
		DialTimeout: time.Millisecond,
		ReadTimeout: time.Millisecond,
	})}
	defer adapter.Client.Close()
	limiter := NewRedisRateLimiter(adapter, 1, time.Second)

	if !limiter.Allow(context.Background(), "client") {
		t.Fatal("expected unavailable redis to allow by default")
	}
}

func TestWebSocketOriginPolicyUsesAllowlist(t *testing.T) {
	cfg := NewSysConfig()
	cfg.WS.AllowedOrigins = []string{"https://app.example.com"}

	allowed := httptest.NewRequest(http.MethodGet, "/ws", nil)
	allowed.Header.Set("Origin", "https://app.example.com")
	if !websocketOriginAllowed(cfg, allowed) {
		t.Fatal("expected allowlisted websocket origin")
	}

	denied := httptest.NewRequest(http.MethodGet, "/ws", nil)
	denied.Header.Set("Origin", "https://evil.example.com")
	if websocketOriginAllowed(cfg, denied) {
		t.Fatal("expected non-allowlisted websocket origin to be denied")
	}
}

func TestIgniteRejectsDisabledWebSocketOriginCheckInProduction(t *testing.T) {
	resetTestInjector()
	resetGinModeForTest(t)
	cfg := NewSysConfig()
	cfg.DB.Enabled = false
	cfg.Server.Mode = "release"
	cfg.Auth.JWTSecret = "replace-with-at-least-32-random-characters"
	cfg.WS.CheckOrigin = false

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected unsafe websocket origin panic")
		}
		if !strings.Contains(r.(string), "websocket origin") {
			t.Fatalf("unexpected panic: %v", r)
		}
	}()

	Ignite(cfg)
}

func TestProductionValidationChecksWebSocketOriginWhenAuthConfigIsNil(t *testing.T) {
	resetTestInjector()
	resetGinModeForTest(t)
	cfg := NewSysConfig()
	cfg.Auth = nil
	cfg.DB.Enabled = false
	cfg.Server.Mode = "release"
	cfg.WS.CheckOrigin = false

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected unsafe websocket origin panic")
		}
		if !strings.Contains(r.(string), "websocket origin") {
			t.Fatalf("unexpected panic: %v", r)
		}
	}()

	Ignite(cfg)
}

type repositoryUpdateTestModel struct {
	ID   uint `gorm:"primaryKey"`
	Name string
	Note string
}

func TestRepositoryUpdateDoesNotOverwriteOmittedFieldsWithZeroValues(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&repositoryUpdateTestModel{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	adapter := &GormAdapter{DB: db}
	repo := NewRepository[repositoryUpdateTestModel](adapter)

	original := repositoryUpdateTestModel{Name: "alice", Note: "keep"}
	if err := db.Create(&original).Error; err != nil {
		t.Fatalf("create: %v", err)
	}

	patch := repositoryUpdateTestModel{ID: original.ID, Name: "bob"}
	if err := repo.Update(context.Background(), &patch); err != nil {
		t.Fatalf("update: %v", err)
	}

	var got repositoryUpdateTestModel
	if err := db.First(&got, original.ID).Error; err != nil {
		t.Fatalf("find: %v", err)
	}
	if got.Name != "bob" {
		t.Fatalf("name = %q", got.Name)
	}
	if got.Note != "keep" {
		t.Fatalf("note was overwritten: %#v", got)
	}
}

func TestLoadSQLMigrationsSortsUpAndDownFiles(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir+"/002_add_email.up.sql", "ALTER TABLE users ADD COLUMN email TEXT;")
	writeTestFile(t, dir+"/001_create_users.up.sql", "CREATE TABLE users (id INTEGER PRIMARY KEY);")
	writeTestFile(t, dir+"/001_create_users.down.sql", "DROP TABLE users;")

	migrations, err := LoadSQLMigrations(dir)
	if err != nil {
		t.Fatalf("load migrations: %v", err)
	}

	if len(migrations) != 2 {
		t.Fatalf("migration count = %d", len(migrations))
	}
	if migrations[0].Version != "001" || migrations[0].Name != "create_users" {
		t.Fatalf("first migration = %#v", migrations[0])
	}
	if migrations[0].DownSQL == "" {
		t.Fatalf("expected down sql to be loaded: %#v", migrations[0])
	}
	if migrations[1].Version != "002" || migrations[1].Name != "add_email" {
		t.Fatalf("second migration = %#v", migrations[1])
	}
}

func TestMigrationRunnerAppliesMigrationsIdempotently(t *testing.T) {
	sqlDB := newMigrationTestDB(t)
	runner := NewMigrationRunner(sqlDB)
	migrations := []Migration{
		{Version: "001", Name: "create_users", UpSQL: "CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT);"},
		{Version: "002", Name: "add_email", UpSQL: "ALTER TABLE users ADD COLUMN email TEXT;"},
	}

	if err := runner.Up(context.Background(), migrations); err != nil {
		t.Fatalf("first up: %v", err)
	}
	if err := runner.Up(context.Background(), migrations); err != nil {
		t.Fatalf("second up should be idempotent: %v", err)
	}

	var applied int
	if err := sqlDB.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM schema_migrations").Scan(&applied); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if applied != 2 {
		t.Fatalf("applied count = %d", applied)
	}
	if _, err := sqlDB.ExecContext(context.Background(), "INSERT INTO users (name, email) VALUES ('alice', 'a@example.com')"); err != nil {
		t.Fatalf("users table should exist with email column: %v", err)
	}
}

func TestMigrationRunnerStopsOnInvalidSQL(t *testing.T) {
	sqlDB := newMigrationTestDB(t)
	runner := NewMigrationRunner(sqlDB)
	migrations := []Migration{
		{Version: "001", Name: "broken", UpSQL: "CREATE TABLE broken ("},
	}

	if err := runner.Up(context.Background(), migrations); err == nil {
		t.Fatal("expected invalid SQL error")
	}

	var applied int
	if err := sqlDB.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM schema_migrations").Scan(&applied); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if applied != 0 {
		t.Fatalf("applied count after failure = %d", applied)
	}
}

func TestMigrationRunnerRollsBackLatestMigrations(t *testing.T) {
	sqlDB := newMigrationTestDB(t)
	runner := NewMigrationRunner(sqlDB)
	migrations := []Migration{
		{
			Version: "001",
			Name:    "create_users",
			UpSQL:   "CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT);",
			DownSQL: "DROP TABLE users;",
		},
		{
			Version: "002",
			Name:    "create_audit",
			UpSQL:   "CREATE TABLE audit_logs (id INTEGER PRIMARY KEY, message TEXT);",
			DownSQL: "DROP TABLE audit_logs;",
		},
	}

	if err := runner.Up(context.Background(), migrations); err != nil {
		t.Fatalf("up: %v", err)
	}
	if err := runner.Down(context.Background(), migrations, 1); err != nil {
		t.Fatalf("down: %v", err)
	}

	if _, err := sqlDB.ExecContext(context.Background(), "INSERT INTO audit_logs (message) VALUES ('rolled back')"); err == nil {
		t.Fatal("expected audit_logs table to be removed after rollback")
	}
	if _, err := sqlDB.ExecContext(context.Background(), "INSERT INTO users (name) VALUES ('alice')"); err != nil {
		t.Fatalf("users table should remain after one-step rollback: %v", err)
	}
	var applied int
	if err := sqlDB.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM schema_migrations").Scan(&applied); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if applied != 1 {
		t.Fatalf("applied count after rollback = %d", applied)
	}
}

func TestMigrationRunnerUsesExecutionLock(t *testing.T) {
	sqlDB := newMigrationTestDB(t)
	runner := NewMigrationRunner(sqlDB)
	if err := runner.ensureLockTable(context.Background(), "schema_migration_locks"); err != nil {
		t.Fatalf("ensure lock table: %v", err)
	}
	if _, err := sqlDB.ExecContext(context.Background(), "INSERT INTO schema_migration_locks (name) VALUES (?)", defaultMigrationLockName); err != nil {
		t.Fatalf("insert held lock: %v", err)
	}

	err := runner.Up(context.Background(), []Migration{
		{Version: "001", Name: "create_users", UpSQL: "CREATE TABLE users (id INTEGER PRIMARY KEY);"},
	})
	if err == nil {
		t.Fatal("expected migration lock error")
	}
	if !strings.Contains(err.Error(), "migration lock") {
		t.Fatalf("unexpected lock error: %v", err)
	}
}

func TestMigrationRunnerForceUnlockReleasesHeldLock(t *testing.T) {
	sqlDB := newMigrationTestDB(t)
	runner := NewMigrationRunner(sqlDB)
	if err := runner.ensureLockTable(context.Background(), "schema_migration_locks"); err != nil {
		t.Fatalf("ensure lock table: %v", err)
	}
	if _, err := sqlDB.ExecContext(context.Background(), "INSERT INTO schema_migration_locks (name) VALUES (?)", defaultMigrationLockName); err != nil {
		t.Fatalf("insert held lock: %v", err)
	}

	if err := runner.ForceUnlock(context.Background()); err != nil {
		t.Fatalf("force unlock: %v", err)
	}
	if err := runner.Up(context.Background(), []Migration{
		{Version: "001", Name: "create_users", UpSQL: "CREATE TABLE users (id INTEGER PRIMARY KEY);"},
	}); err != nil {
		t.Fatalf("up after force unlock: %v", err)
	}
}

func writeTestFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func newMigrationTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("sql db: %v", err)
	}
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Fatal(err)
		}
	})
	return sqlDB
}

func TestLoadPluginDisabledByDefault(t *testing.T) {
	resetTestInjector()
	resetGinModeForTest(t)
	cfg := NewSysConfig()
	cfg.DB.Enabled = false
	app := Ignite(cfg)

	err := app.LoadPlugin("/tmp/example.so")

	if err == nil {
		t.Fatal("expected plugin loading to be disabled by default")
	}
	if !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadPluginRejectsPathOutsideAllowlist(t *testing.T) {
	resetTestInjector()
	resetGinModeForTest(t)
	cfg := NewSysConfig()
	cfg.DB.Enabled = false
	cfg.Plugins.Enabled = true
	cfg.Plugins.AllowedDirs = []string{"/opt/gin-bear/plugins"}
	app := Ignite(cfg)

	err := app.LoadPlugin("/tmp/example.so")

	if err == nil {
		t.Fatal("expected plugin outside allowlist to be rejected")
	}
	if !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBuildGormConfigAppliesSlowQueryLogger(t *testing.T) {
	cfg := &DBConfig{SlowQueryThreshold: "250ms"}

	gormCfg := buildGormConfig(cfg)

	if gormCfg.Logger == nil {
		t.Fatal("expected gorm logger to be configured")
	}
}

func TestApplyEnvOverridesProductionSecretsAndDependencies(t *testing.T) {
	cfg := NewSysConfig()
	t.Setenv("JWT_SECRET", "env-secret-with-at-least-32-characters")
	t.Setenv("REDIS_ADDR", "redis.example:6379")
	t.Setenv("POSTGRES_HOST", "db.example")
	t.Setenv("POSTGRES_PASSWORD", "db-secret")

	applyEnvOverrides(cfg)

	if cfg.Auth.JWTSecret != "env-secret-with-at-least-32-characters" {
		t.Fatalf("jwt secret = %q", cfg.Auth.JWTSecret)
	}
	if cfg.Redis.Addr != "redis.example:6379" {
		t.Fatalf("redis addr = %q", cfg.Redis.Addr)
	}
	if cfg.DB.Host != "db.example" {
		t.Fatalf("db host = %q", cfg.DB.Host)
	}
	if cfg.DB.Password != "db-secret" {
		t.Fatalf("db password = %q", cfg.DB.Password)
	}
}

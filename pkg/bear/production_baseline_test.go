package bear

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"
	"gorm.io/gorm"
)

var _ interface {
	WithAttrs([]slog.Attr) slog.Handler
	WithGroup(string) slog.Handler
} = ContextHandler{}

func randomProductionJWTKey(t *testing.T) string {
	t.Helper()
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatalf("generate production JWT test key: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

func resetTestInjector() {
	setDefaultInjector(NewBeanFactory())
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

func TestIgniteEReturnsValidationError(t *testing.T) {
	t.Setenv("BEAR_ENV", "dev")
	cfg := NewSysConfig()
	cfg.Server.ReadTimeout = "not-a-duration"

	app, err := IgniteE(cfg)
	if err == nil || !strings.Contains(err.Error(), "server.read_timeout") {
		t.Fatalf("IgniteE error = %v, want server.read_timeout validation error", err)
	}
	if app != nil {
		t.Fatal("IgniteE returned an app with invalid configuration")
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
		{
			name: "shutdown timeout must parse",
			mutate: func(cfg *SysConfig) {
				cfg.Server.ShutdownTimeout = "later"
			},
			wantErr: "server.shutdown_timeout",
		},
		{
			name: "readiness timeout must parse",
			mutate: func(cfg *SysConfig) {
				cfg.Health.ReadinessTimeout = "eventually"
			},
			wantErr: "health.readiness_timeout",
		},
		{
			name: "log level must be supported",
			mutate: func(cfg *SysConfig) {
				cfg.Log.Level = "verbose"
			},
			wantErr: "log.level",
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

func TestRuntimeTimeoutHelpersUseConfiguredValues(t *testing.T) {
	cfg := NewSysConfig()
	cfg.Server.ShutdownTimeout = "12s"
	cfg.Health.ReadinessTimeout = "1500ms"

	if got := shutdownTimeout(cfg); got != 12*time.Second {
		t.Fatalf("shutdown timeout = %s", got)
	}
	if got := readinessTimeout(cfg); got != 1500*time.Millisecond {
		t.Fatalf("readiness timeout = %s", got)
	}
}

type webSocketTerminalFairing struct{ BaseFairing }

func (f *webSocketTerminalFairing) OnRequest(ctx *gin.Context) error {
	ctx.String(http.StatusUnauthorized, "unauthorized")
	return nil
}

type webSocketTerminalHandler struct {
	BaseWebSocketHandler
	connected bool
}

func (h *webSocketTerminalHandler) OnConnect(*gin.Context, *websocket.Conn) error {
	h.connected = true
	return nil
}

func TestWebSocketFairingTerminal(t *testing.T) {
	app := Ignite(NewSysConfig())
	app.Attach(&webSocketTerminalFairing{})
	handler := &webSocketTerminalHandler{}
	app.HandleWS("/ws", handler)
	server := httptest.NewServer(app)
	defer server.Close()

	connection, response, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws", nil)
	if connection != nil {
		connection.Close()
		t.Fatal("WebSocket upgraded after Fairing wrote a response")
	}
	if err == nil {
		t.Fatal("WebSocket dial succeeded after Fairing wrote a response")
	}
	if response == nil || response.StatusCode != http.StatusUnauthorized {
		status := 0
		if response != nil {
			status = response.StatusCode
		}
		t.Fatalf("WebSocket response status = %d, want %d", status, http.StatusUnauthorized)
	}
	if handler.connected {
		t.Fatal("WebSocket handler ran after Fairing wrote a response")
	}
}

func TestSetDefaultLoggerUsesConfiguredLevel(t *testing.T) {
	cfg := NewSysConfig()
	cfg.Log.Level = "debug"

	setDefaultLoggerForConfig(cfg)
	t.Cleanup(func() {
		setDefaultLoggerForConfig(NewSysConfig())
	})

	if !slog.Default().Enabled(context.Background(), slog.LevelDebug) {
		t.Fatal("expected debug logging to be enabled")
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

func TestParseConfigSupportsStructuredInputs(t *testing.T) {
	type sampleConfig struct {
		Name  string `json:"name" yaml:"name"`
		Count int    `json:"count" yaml:"count"`
	}

	t.Run("yaml extension", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "application.yaml")
		if err := os.WriteFile(path, []byte("name: yaml\ncount: 2\n"), 0644); err != nil {
			t.Fatal(err)
		}

		var got sampleConfig
		if err := ParseConfig(path, &got); err != nil {
			t.Fatalf("ParseConfig yaml failed: %v", err)
		}
		if got.Name != "yaml" || got.Count != 2 {
			t.Fatalf("unexpected yaml config: %#v", got)
		}
	})

	t.Run("json without extension", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "application")
		if err := os.WriteFile(path, []byte(`{"name":"json","count":3}`), 0644); err != nil {
			t.Fatal(err)
		}

		var got sampleConfig
		if err := ParseConfig(path, &got); err != nil {
			t.Fatalf("ParseConfig json detection failed: %v", err)
		}
		if got.Name != "json" || got.Count != 3 {
			t.Fatalf("unexpected json config: %#v", got)
		}
	})

	t.Run("yaml without extension", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "application")
		if err := os.WriteFile(path, []byte("name: fallback\ncount: 7\n"), 0644); err != nil {
			t.Fatal(err)
		}

		var got sampleConfig
		if err := ParseConfig(path, &got); err != nil {
			t.Fatalf("ParseConfig yaml detection failed: %v", err)
		}
		if got.Name != "fallback" || got.Count != 7 {
			t.Fatalf("unexpected fallback config: %#v", got)
		}
	})
}

func TestParseConfigReturnsUsefulErrorsForMissingAndInvalidFiles(t *testing.T) {
	t.Run("missing file", func(t *testing.T) {
		err := ParseConfig(filepath.Join(t.TempDir(), "missing.yaml"), &map[string]interface{}{})
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("ParseConfig() error = %v, want not-exist error", err)
		}
	})

	t.Run("invalid yaml", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "application.yaml")
		if err := os.WriteFile(path, []byte("name: ["), 0644); err != nil {
			t.Fatal(err)
		}

		err := ParseConfig(path, &map[string]interface{}{})
		if err == nil || !strings.Contains(err.Error(), "failed to parse YAML config") {
			t.Fatalf("ParseConfig() error = %v", err)
		}
	})

	t.Run("invalid json", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "application.json")
		if err := os.WriteFile(path, []byte(`{"name":`), 0644); err != nil {
			t.Fatal(err)
		}

		err := ParseConfig(path, &map[string]interface{}{})
		if err == nil || !strings.Contains(err.Error(), "failed to parse JSON config") {
			t.Fatalf("ParseConfig() error = %v", err)
		}
	})
}

func TestGetAbsPathAndJoinRootResolveExistingRelativePaths(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(target, []byte("name: test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(wd); err != nil {
			t.Fatal(err)
		}
	})

	want, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	for name, gotPath := range map[string]string{
		"GetAbsPath": GetAbsPath("config.yaml"),
		"JoinRoot":   JoinRoot(".", "config.yaml"),
	} {
		got, err := filepath.EvalSymlinks(gotPath)
		if err != nil {
			t.Fatalf("%s EvalSymlinks failed: %v", name, err)
		}
		if got != want {
			t.Fatalf("%s() = %q, want %q", name, got, want)
		}
	}
}

func TestGetAbsPathHandlesAbsoluteAndMissingPaths(t *testing.T) {
	absolute := filepath.Join(t.TempDir(), "existing.txt")
	if err := os.WriteFile(absolute, []byte("ok"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := GetAbsPath(absolute); got != absolute {
		t.Fatalf("GetAbsPath(absolute) = %q", got)
	}

	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(wd); err != nil {
			t.Fatal(err)
		}
	})

	want, err := filepath.Abs("missing.txt")
	if err != nil {
		t.Fatal(err)
	}
	if got := GetAbsPath("missing.txt"); got != want {
		t.Fatalf("GetAbsPath(missing) = %q, want %q", got, want)
	}
}

func TestWriteFileAtomicReplacesContentWithPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")

	if err := WriteFileAtomic(path, []byte("first"), 0600); err != nil {
		t.Fatalf("initial WriteFileAtomic failed: %v", err)
	}
	if err := WriteFileAtomic(path, []byte("second"), 0640); err != nil {
		t.Fatalf("replacement WriteFileAtomic failed: %v", err)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "second" {
		t.Fatalf("file content = %q", string(content))
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0640 {
		t.Fatalf("file mode = %o", info.Mode().Perm())
	}
}

func TestWriteFileAtomicReturnsErrorWhenParentIsNotADirectory(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(parent, []byte("file"), 0644); err != nil {
		t.Fatal(err)
	}

	err := WriteFileAtomic(filepath.Join(parent, "config.yaml"), []byte("data"), 0644)
	if err == nil || !strings.Contains(err.Error(), "failed to create temp file") {
		t.Fatalf("WriteFileAtomic() error = %v", err)
	}
}

func TestValueHelpersExposeConfiguredTypes(t *testing.T) {
	tests := []struct {
		name      string
		value     interface{}
		wantStr   string
		wantInt   int
		wantInt64 int64
		wantFloat float64
		wantBool  bool
	}{
		{
			name:      "int64 value",
			value:     int64(42),
			wantStr:   "42",
			wantInt:   42,
			wantInt64: 42,
		},
		{
			name:      "int value",
			value:     int(7),
			wantStr:   "7",
			wantInt:   7,
			wantInt64: 7,
		},
		{
			name:      "int32 value",
			value:     int32(8),
			wantStr:   "8",
			wantInt:   8,
			wantInt64: 8,
		},
		{
			name:      "float value",
			value:     9.5,
			wantStr:   "9.5",
			wantInt:   9,
			wantInt64: 9,
			wantFloat: 9.5,
		},
		{
			name:     "bool value",
			value:    true,
			wantStr:  "true",
			wantBool: true,
		},
		{
			name:    "string value",
			value:   "bear",
			wantStr: "bear",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := NewValue("server", "port")
			v.value = tt.value

			if got := v.GetString(); got != tt.wantStr {
				t.Fatalf("GetString() = %q, want %q", got, tt.wantStr)
			}
			if got := v.GetInt(); got != tt.wantInt {
				t.Fatalf("GetInt() = %d, want %d", got, tt.wantInt)
			}
			if got := v.GetInt64(); got != tt.wantInt64 {
				t.Fatalf("GetInt64() = %d, want %d", got, tt.wantInt64)
			}
			if got := v.GetFloat(); got != tt.wantFloat {
				t.Fatalf("GetFloat() = %f, want %f", got, tt.wantFloat)
			}
			if got := v.GetBool(); got != tt.wantBool {
				t.Fatalf("GetBool() = %t, want %t", got, tt.wantBool)
			}
			if got := v.String(); got != tt.wantStr {
				t.Fatalf("String() = %q, want %q", got, tt.wantStr)
			}
		})
	}
}

func TestValueHelpersReturnZeroValuesForNilAndUnsupportedValues(t *testing.T) {
	var nilValue *Value
	if nilValue.GetString() != "" || nilValue.GetInt() != 0 || nilValue.GetInt64() != 0 || nilValue.GetFloat() != 0 || nilValue.GetBool() {
		t.Fatal("nil Value should return zero values")
	}

	v := NewValue("server", "port")
	v.value = struct{}{}
	if v.GetInt() != 0 || v.GetInt64() != 0 || v.GetFloat() != 0 || v.GetBool() {
		t.Fatalf("unsupported value should return zero values: %#v", v.value)
	}
}

func TestGetConfigValueReadsFlatKeysOnly(t *testing.T) {
	cfg := NewSysConfig()
	cfg.Config = map[string]interface{}{
		"server.port": 8080,
	}

	if got := getConfigValue(cfg, "server.port"); got != 8080 {
		t.Fatalf("getConfigValue(server.port) = %#v", got)
	}
	if got := getConfigValue(cfg, "server.name"); got != nil {
		t.Fatalf("expected missing key to return nil, got %#v", got)
	}
}

func TestRouteTreeMatchesStaticWildcardAndCatchAllRoutes(t *testing.T) {
	rootFairing := &BaseFairing{}
	staticFairing := &BaseFairing{}
	paramFairing := &BaseFairing{}
	catchAllFairing := &BaseFairing{}

	tree := NewRouteTree()
	tree.addRoute(http.MethodGet, "/", []Fairing{rootFairing})
	tree.addRoute(http.MethodGet, "/users/list", []Fairing{staticFairing})
	tree.addRoute(http.MethodGet, "/users/:id", []Fairing{paramFairing})
	tree.addRoute(http.MethodGet, "/assets/*filepath", []Fairing{catchAllFairing})

	if got := tree.getRoute(http.MethodGet, "/users/list"); len(got) != 1 || got[0] != staticFairing {
		t.Fatalf("static route = %#v", got)
	}
	if got := tree.getRoute(http.MethodGet, "/users/42"); len(got) != 1 || got[0] != paramFairing {
		t.Fatalf("param route = %#v", got)
	}
	if got := tree.getRoute(http.MethodGet, "/assets/css/app.css"); len(got) != 1 || got[0] != catchAllFairing {
		t.Fatalf("catch-all route = %#v", got)
	}
	if got := tree.getRoute(http.MethodGet, "/"); len(got) != 1 || got[0] != rootFairing {
		t.Fatalf("root route = %#v", got)
	}
	if got := tree.getRoute(http.MethodGet, "/missing"); got != nil {
		t.Fatalf("expected unmatched route to return nil, got %#v", got)
	}
	if got := tree.getRoute(http.MethodPost, "/users/list"); got != nil {
		t.Fatalf("expected method-specific tree miss, got %#v", got)
	}
}

func TestPluginDispatcherOverridesAndRestoresRegisteredRoutes(t *testing.T) {
	dispatcher := NewPluginDispatcher()
	dispatcher.Register(http.MethodGet, "/plugins/:name", func(c *gin.Context) {
		c.String(http.StatusOK, "plugin")
	})

	router := gin.New()
	router.Use(dispatcher.Dispatch())
	router.GET("/plugins/:name", func(c *gin.Context) {
		c.String(http.StatusOK, "fallback")
	})

	request := httptest.NewRequest(http.MethodGet, "/plugins/bear", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.String() != "plugin" {
		t.Fatalf("registered route response = %d %q", response.Code, response.Body.String())
	}

	dispatcher.Unregister(http.MethodGet, "/plugins/:name")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.String() != "fallback" {
		t.Fatalf("unregistered route response = %d %q", response.Code, response.Body.String())
	}
}

func TestResponseHelpersReturnStablePayloads(t *testing.T) {
	result := Result(201, "created", map[string]string{"id": "1"})
	if result.Code != 201 || result.Message != "created" {
		t.Fatalf("Result() = %#v", result)
	}

	success := Success("ok")
	if success.Code != 200 || success.Message != "success" || success.Data != "ok" {
		t.Fatalf("Success() = %#v", success)
	}

	failure := Error(400, "bad request")
	if failure.Code != 400 || failure.Message != "bad request" || failure.Data != nil {
		t.Fatalf("Error() = %#v", failure)
	}
}

func TestBearErrorHelpersPreserveBusinessMetadata(t *testing.T) {
	cause := errors.New("disk full")
	base := &BearError{Code: 409, Status: http.StatusConflict, Message: "conflict", Key: "error_conflict"}

	withMsg := base.WithMsg("duplicate")
	if withMsg.Message != "duplicate" || base.Message != "conflict" {
		t.Fatalf("WithMsg() = %#v, base = %#v", withMsg, base)
	}

	withErr := base.WithErr(cause)
	if !errors.Is(withErr, cause) {
		t.Fatalf("WithErr should unwrap cause: %v", withErr)
	}
	if withErr.Unwrap() != cause {
		t.Fatalf("Unwrap() = %v", withErr.Unwrap())
	}
	if !withErr.Is(&BearError{Code: 409}) {
		t.Fatalf("Is() should match on error code")
	}

	withArgs := base.WithArgs("user", 7)
	if len(withArgs.Args) != 2 {
		t.Fatalf("WithArgs() = %#v", withArgs.Args)
	}

	response := withMsg.ToResponse()
	if response.Code != 409 || response.Message != "duplicate" {
		t.Fatalf("ToResponse() = %#v", response)
	}

	registry := &ErrorRegistry{errors: map[int]*BearError{}}
	path := filepath.Join(t.TempDir(), "errors.yaml")
	if err := os.WriteFile(path, []byte("- code: 499\n  status: 409\n  key: error_custom\n  message: custom message\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := registry.LoadErrorsFromYAML(path); err != nil {
		t.Fatalf("LoadErrorsFromYAML failed: %v", err)
	}
	if got := registry.errors[499]; got == nil || got.Message != "custom message" {
		t.Fatalf("loaded registry entry = %#v", got)
	}
	if err := registry.LoadErrorsFromYAML(filepath.Join(t.TempDir(), "missing.yaml")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("LoadErrorsFromYAML(missing) error = %v", err)
	}
	invalidPath := filepath.Join(t.TempDir(), "invalid.yaml")
	if err := os.WriteFile(invalidPath, []byte("["), 0644); err != nil {
		t.Fatal(err)
	}
	if err := registry.LoadErrorsFromYAML(invalidPath); err == nil {
		t.Fatal("LoadErrorsFromYAML(invalid) returned nil error")
	}

	if got := GetDefaultRegistry(); got == nil {
		t.Fatal("GetDefaultRegistry() returned nil")
	}
	if got := GetError(http.StatusNotFound, "resource"); got.Code != http.StatusNotFound || len(got.Args) != 1 {
		t.Fatalf("GetError(known) = %#v", got)
	}
	if got := GetError(9999, "arg"); got.Code != 9999 || got.Status != http.StatusInternalServerError {
		t.Fatalf("GetError(unknown) = %#v", got)
	}
	if got := NewError(418, "teapot", "x"); got.Code != 418 || got.Key != "teapot" {
		t.Fatalf("NewError() = %#v", got)
	}
	if msg := (&BearError{Code: 400, Message: "bad"}).Error(); !strings.Contains(msg, "Error 400: bad") {
		t.Fatalf("Error() = %q", msg)
	}
	if msg := (&BearError{Code: 401, Key: "unauthorized", Err: cause}).Error(); !strings.Contains(msg, "key: unauthorized") || !strings.Contains(msg, "disk full") {
		t.Fatalf("Error with key = %q", msg)
	}
}

func TestParseLogLevelAndWithContextHandleDefaults(t *testing.T) {
	tests := map[string]slog.Level{
		"debug":   slog.LevelDebug,
		"warning": slog.LevelWarn,
		"error":   slog.LevelError,
		"other":   slog.LevelInfo,
	}
	for raw, want := range tests {
		if got := parseLogLevel(raw); got != want {
			t.Fatalf("parseLogLevel(%q) = %v, want %v", raw, got, want)
		}
	}

	if got := WithContext(context.TODO()); got == nil {
		t.Fatal("WithContext(context.TODO()) returned nil logger")
	}

	ctx := context.WithValue(context.Background(), RequestIDKey, "req-123")
	logger := WithContext(ctx)
	if logger == nil {
		t.Fatal("WithContext(ctx) returned nil logger")
	}
}

func TestContextHandlerAndLogWrappersDoNotPanic(t *testing.T) {
	var sink bytes.Buffer
	logger := slog.New(&ContextHandler{
		Handler: slog.NewTextHandler(&sink, nil),
	})
	record := slog.NewRecord(time.Now(), slog.LevelInfo, "hello", 0)

	if err := logger.Handler().Handle(context.Background(), record); err != nil {
		t.Fatalf("Handle(background) failed: %v", err)
	}

	ctx := context.WithValue(context.Background(), RequestIDKey, "req-ctx")
	recordWithRID := slog.NewRecord(time.Now(), slog.LevelInfo, "world", 0)
	if err := logger.Handler().Handle(ctx, recordWithRID); err != nil {
		t.Fatalf("Handle(ctx) failed: %v", err)
	}

	setDefaultLoggerForConfig(NewSysConfig())
	Info("info")
	ErrorLog("error")
	Warn("warn")
	Debug("debug")
	InfoContext(ctx, "info ctx")
	ErrorContext(ctx, "error ctx")
	WarnContext(ctx, "warn ctx")
	DebugContext(ctx, "debug ctx")
}

func TestAuthTokenManagerNameAndBlacklistKeyAreStable(t *testing.T) {
	manager := NewAuthTokenManager()
	if manager.Name() != "AuthTokenManager" {
		t.Fatalf("Name() = %q", manager.Name())
	}

	key1 := manager.blacklistKey("token")
	key2 := manager.blacklistKey("token")
	key3 := manager.blacklistKey("other")
	if key1 != key2 {
		t.Fatalf("blacklistKey should be deterministic: %q vs %q", key1, key2)
	}
	if key1 == key3 {
		t.Fatalf("blacklistKey should vary with token")
	}
}

func TestJWTUtilGeneratesAndParsesHS256Tokens(t *testing.T) {
	util := NewJWTUtil("secret-1234567890", 1)
	if util.Name() != "JWTUtil" {
		t.Fatalf("Name() = %q", util.Name())
	}

	token, err := util.GenerateToken(7, "bear@example.com")
	if err != nil {
		t.Fatalf("GenerateToken failed: %v", err)
	}

	claims, err := util.ParseToken(token)
	if err != nil {
		t.Fatalf("ParseToken failed: %v", err)
	}
	if claims.UserID != 7 || claims.Email != "bear@example.com" {
		t.Fatalf("unexpected claims: %#v", claims)
	}
}

func TestJWTUtilRejectsUnexpectedSigningMethodWithGeneratedNoneToken(t *testing.T) {
	util := NewJWTUtil("secret-1234567890", 1)
	token := jwt.NewWithClaims(jwt.SigningMethodNone, &CustomClaims{UserID: 1})
	tokenStr, err := token.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("SignedString failed: %v", err)
	}

	if _, err := util.ParseToken(tokenStr); err == nil || !strings.Contains(err.Error(), "unexpected signing method") {
		t.Fatalf("ParseToken error = %v", err)
	}
}

func TestMemoryRateLimiterHonorsLimitAndStop(t *testing.T) {
	limiter := NewMemoryRateLimiter(2, time.Hour)
	defer limiter.Stop()

	if limiter.Name() != "MemoryRateLimiter" {
		t.Fatalf("Name() = %q", limiter.Name())
	}
	if !limiter.Allow(context.Background(), "127.0.0.1") {
		t.Fatal("first request should pass")
	}
	if !limiter.Allow(context.Background(), "127.0.0.1") {
		t.Fatal("second request should pass")
	}
	if limiter.Allow(context.Background(), "127.0.0.1") {
		t.Fatal("third request should be limited")
	}
	limiter.Stop()
}

func TestRedisRateLimiterFallbackAndName(t *testing.T) {
	openLimiter := NewRedisRateLimiter(nil, 3, time.Second)
	if openLimiter.Name() != "RedisRateLimiter" {
		t.Fatalf("Name() = %q", openLimiter.Name())
	}
	if !openLimiter.Allow(context.Background(), "ip") {
		t.Fatal("nil adapter should fail open by default")
	}

	closedLimiter := NewRedisRateLimiter(nil, 3, time.Second)
	closedLimiter.FailClosed = true
	if closedLimiter.Allow(context.Background(), "ip") {
		t.Fatal("nil adapter should fail closed when configured")
	}
}

func TestRateLimitMiddlewareReturns429WhenLimiterBlocks(t *testing.T) {
	limiter := NewMemoryRateLimiter(0, time.Hour)
	defer limiter.Stop()

	router := gin.New()
	router.Use(RateLimitMiddleware(limiter))
	router.GET("/limited", func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	req := httptest.NewRequest(http.MethodGet, "/limited", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Too many requests") {
		t.Fatalf("body = %s", w.Body.String())
	}
}

func TestGzipMiddlewareCompressesSupportedResponses(t *testing.T) {
	router := gin.New()
	router.Use(GzipMiddleware(128))
	router.GET("/data", func(c *gin.Context) {
		c.String(http.StatusOK, strings.Repeat("a", 32))
	})

	req := httptest.NewRequest(http.MethodGet, "/data", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if got := w.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q", got)
	}
	if got := w.Header().Get("Vary"); got != "Accept-Encoding" {
		t.Fatalf("Vary = %q", got)
	}
}

func TestGzipWriterWriteStringCompressesText(t *testing.T) {
	var compressed bytes.Buffer
	zw := gzip.NewWriter(&compressed)
	writer := &gzipWriter{writer: zw}
	if _, err := writer.WriteString("hello gzip"); err != nil {
		t.Fatalf("WriteString failed: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	zr, err := gzip.NewReader(bytes.NewReader(compressed.Bytes()))
	if err != nil {
		t.Fatalf("NewReader failed: %v", err)
	}
	defer zr.Close()
	data, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}
	if string(data) != "hello gzip" {
		t.Fatalf("decompressed = %q", string(data))
	}
}

func TestMiddlewareHelpersCoverOriginChecksAndRequestIDs(t *testing.T) {
	if !corsOriginAllowed("https://example.com", []string{"https://example.com"}, true) {
		t.Fatal("exact origin should pass")
	}
	if corsOriginAllowed("https://example.com", []string{"*"}, true) {
		t.Fatal("wildcard with credentials should not pass")
	}
	if !corsOriginAllowed("https://example.com", []string{"*"}, false) {
		t.Fatal("wildcard without credentials should pass")
	}

	router := gin.New()
	router.Use(RequestIDMiddleware())
	router.GET("/id", func(c *gin.Context) {
		c.String(http.StatusOK, c.GetString(string(RequestIDKey)))
	})

	req := httptest.NewRequest(http.MethodGet, "/id", nil)
	req.Header.Set("X-Request-ID", "req-1")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if got := w.Header().Get("X-Request-ID"); got != "req-1" {
		t.Fatalf("header request id = %q", got)
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
	cfg.Auth.JWTSecret = randomProductionJWTKey(t)

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

func TestIgniteRejectsScaffoldJWTPlaceholderInRelease(t *testing.T) {
	resetTestInjector()
	resetGinModeForTest(t)
	cfg := NewSysConfig()
	cfg.DB.Enabled = false
	cfg.Server.Mode = "release"
	cfg.Auth.JWTSecret = "replace-with-at-least-32-random-characters"

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected scaffold JWT placeholder panic")
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
	cfg.Auth.JWTSecret = randomProductionJWTKey(t)

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

func TestGenerateOpenAPIUsesDefaultTitleWithoutRegisteredConfig(t *testing.T) {
	resetTestInjector()

	doc, err := (&Bear{}).GenerateOpenAPI()
	if err != nil {
		t.Fatalf("generate openapi: %v", err)
	}
	var spec map[string]interface{}
	if err := json.Unmarshal(doc, &spec); err != nil {
		t.Fatalf("decode openapi: %v\n%s", err, string(doc))
	}
	info := spec["info"].(map[string]interface{})
	if info["title"] != "gin-bear" {
		t.Fatalf("title = %q, want gin-bear", info["title"])
	}
}

func TestGenerateOpenAPIIncludesJWTSecurityScheme(t *testing.T) {
	resetTestInjector()
	resetGinModeForTest(t)
	cfg := NewSysConfig()
	cfg.DB.Enabled = false
	cfg.Server.Name = "secure-openapi-test"
	cfg.Auth.JWTSecret = randomProductionJWTKey(t)

	app := Ignite(cfg)
	app.Attach(NewAuthFairing())
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
	cfg.Auth.PublicPaths = stringSlicePointer("/api/public/*")

	app := Ignite(cfg)
	app.Attach(NewAuthFairing())
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

func TestGenerateOpenAPIIncludesStandardErrorResponses(t *testing.T) {
	resetTestInjector()
	resetGinModeForTest(t)
	cfg := NewSysConfig()
	cfg.DB.Enabled = false
	cfg.Auth.PublicPaths = stringSlicePointer("/api/public/*")

	app := Ignite(cfg)
	app.Attach(NewAuthFairing())
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

	components := spec["components"].(map[string]interface{})
	schemas := components["schemas"].(map[string]interface{})
	if _, ok := schemas["ErrorResponse"]; !ok {
		t.Fatalf("missing ErrorResponse schema: %#v", schemas)
	}
	paths := spec["paths"].(map[string]interface{})
	publicOp := paths["/api/public/ping"].(map[string]interface{})["get"].(map[string]interface{})
	privateOp := paths["/api/private/ping"].(map[string]interface{})["get"].(map[string]interface{})
	for _, status := range []string{"400", "500"} {
		if !openAPIResponseUsesErrorRef(publicOp, status) {
			t.Fatalf("public operation missing %s error ref: %#v", status, publicOp["responses"])
		}
		if !openAPIResponseUsesErrorRef(privateOp, status) {
			t.Fatalf("private operation missing %s error ref: %#v", status, privateOp["responses"])
		}
	}
	if _, ok := publicOp["responses"].(map[string]interface{})["401"]; ok {
		t.Fatalf("public operation should not include 401 response: %#v", publicOp["responses"])
	}
	if !openAPIResponseUsesErrorRef(privateOp, "401") {
		t.Fatalf("private operation missing 401 error ref: %#v", privateOp["responses"])
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

func openAPIResponseUsesErrorRef(op map[string]interface{}, status string) bool {
	responses := op["responses"].(map[string]interface{})
	response, ok := responses[status].(map[string]interface{})
	if !ok {
		return false
	}
	content, ok := response["content"].(map[string]interface{})
	if !ok {
		return false
	}
	jsonContent, ok := content["application/json"].(map[string]interface{})
	if !ok {
		return false
	}
	schema, ok := jsonContent["schema"].(map[string]interface{})
	if !ok {
		return false
	}
	return schema["$ref"] == "#/components/schemas/ErrorResponse"
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
	cfg.Auth.PublicPaths = stringSlicePointer("/public/*")

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
	if privateW.Code != http.StatusUnauthorized {
		t.Fatalf("private status = %d body = %s", privateW.Code, privateW.Body.String())
	}
}

func TestJWTUtilRejectsUnexpectedSigningMethod(t *testing.T) {
	util := NewJWTUtil(randomProductionJWTKey(t), 24)
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

func TestNewRedisAdapterPanicsWhenRequiredRedisUnavailable(t *testing.T) {
	cfg := &RedisConfig{
		Addr:        "127.0.0.1:1",
		DialTimeout: 1,
		ReadTimeout: 1,
		Required:    true,
	}

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected required redis connection panic")
		}
		if !strings.Contains(r.(string), "required redis") {
			t.Fatalf("unexpected panic: %v", r)
		}
	}()

	NewRedisAdapter(cfg)
}

func TestWebSocketOriginPolicyUsesAllowlist(t *testing.T) {
	cfg := NewSysConfig()
	cfg.WS.AllowedOrigins = stringSlicePointer("https://app.example.com")

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
	cfg.Auth.JWTSecret = randomProductionJWTKey(t)
	cfg.WS.CheckOrigin = false

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected unsafe websocket origin panic")
		}
		message, ok := r.(string)
		if !ok || !strings.Contains(message, "websocket origin") {
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
		message, ok := r.(string)
		if !ok || !strings.Contains(message, "websocket origin") {
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
	if err := runner.ensureLockTable(context.Background(), "schema_migration_locks", MigrationDialectSQLite); err != nil {
		t.Fatalf("ensure lock table: %v", err)
	}
	if _, err := sqlDB.ExecContext(context.Background(), "INSERT INTO schema_migration_locks (name, owner) VALUES (?, ?)", defaultMigrationLockName, "held-owner"); err != nil {
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
	if err := runner.ensureLockTable(context.Background(), "schema_migration_locks", MigrationDialectSQLite); err != nil {
		t.Fatalf("ensure lock table: %v", err)
	}
	if _, err := sqlDB.ExecContext(context.Background(), "INSERT INTO schema_migration_locks (name, owner) VALUES (?, ?)", defaultMigrationLockName, "held-owner"); err != nil {
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

func TestMigrationRunnerRejectsUnsafeTableNames(t *testing.T) {
	sqlDB := newMigrationTestDB(t)
	for _, tt := range []struct {
		name      string
		table     string
		lockTable string
	}{
		{name: "migration table", table: "schema_migrations;DROP"},
		{name: "lock table", lockTable: "schema_migration_locks;DROP"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runner := NewMigrationRunner(sqlDB)
			if tt.table != "" {
				runner.Table = tt.table
			}
			if tt.lockTable != "" {
				runner.LockTable = tt.lockTable
			}
			err := runner.Up(context.Background(), []Migration{
				{Version: "001", Name: "create_users", UpSQL: "CREATE TABLE users (id INTEGER PRIMARY KEY);"},
			})
			if err == nil {
				t.Fatal("expected unsafe table name error")
			}
			if !strings.Contains(err.Error(), "invalid migration table name") {
				t.Fatalf("unexpected error: %v", err)
			}
		})
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
	jwtSecret := randomProductionJWTKey(t)
	t.Setenv("JWT_SECRET", jwtSecret)
	t.Setenv("REDIS_ADDR", "redis.example:6379")
	t.Setenv("REDIS_REQUIRED", "true")
	t.Setenv("POSTGRES_HOST", "db.example")
	t.Setenv("POSTGRES_PASSWORD", "db-secret")
	t.Setenv("DB_MAX_OPEN_CONNS", "77")
	t.Setenv("DB_MAX_IDLE_CONNS", "11")
	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("BEAR_SHUTDOWN_TIMEOUT", "13s")
	t.Setenv("BEAR_READINESS_TIMEOUT", "4s")
	t.Setenv("METRICS_PATH", "/internal/metrics")
	t.Setenv("TRACING_EXPORTER", "otlp")
	t.Setenv("TRACING_OTLP_ENDPOINT", "https://otel.example/v1/traces")

	applyEnvOverrides(cfg)

	if cfg.Auth.JWTSecret != jwtSecret {
		t.Fatalf("jwt secret = %q", cfg.Auth.JWTSecret)
	}
	if cfg.Redis.Addr != "redis.example:6379" {
		t.Fatalf("redis addr = %q", cfg.Redis.Addr)
	}
	if !cfg.Redis.Required {
		t.Fatal("expected redis required override")
	}
	if cfg.DB.Host != "db.example" {
		t.Fatalf("db host = %q", cfg.DB.Host)
	}
	if cfg.DB.Password != "db-secret" {
		t.Fatalf("db password = %q", cfg.DB.Password)
	}
	if cfg.DB.MaxOpenConns != 77 || cfg.DB.MaxIdleConns != 11 {
		t.Fatalf("db pool = maxOpen %d maxIdle %d", cfg.DB.MaxOpenConns, cfg.DB.MaxIdleConns)
	}
	if cfg.Log.Level != "debug" {
		t.Fatalf("log level = %q", cfg.Log.Level)
	}
	if cfg.Server.ShutdownTimeout != "13s" {
		t.Fatalf("shutdown timeout = %q", cfg.Server.ShutdownTimeout)
	}
	if cfg.Health.ReadinessTimeout != "4s" {
		t.Fatalf("readiness timeout = %q", cfg.Health.ReadinessTimeout)
	}
	if cfg.Metrics.Path != "/internal/metrics" {
		t.Fatalf("metrics path = %q", cfg.Metrics.Path)
	}
	if cfg.Tracing.Exporter != "otlp" || cfg.Tracing.OTLPEndpoint != "https://otel.example/v1/traces" {
		t.Fatalf("tracing = %#v", cfg.Tracing)
	}
}

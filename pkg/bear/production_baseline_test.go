package bear

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

func resetTestInjector() {
	injector = &BeanFactory{
		beans: make(map[reflect.Type]any),
	}
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

func TestHealthEndpointsExposeLiveAndReady(t *testing.T) {
	resetTestInjector()
	resetGinModeForTest(t)
	cfg := NewSysConfig()
	cfg.DB.Enabled = false

	app := Ignite(cfg)
	app.EnableHealth()
	app.ApplyAll(context.Background())

	for _, path := range []string{"/health", "/live", "/ready"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		app.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s status = %d body = %s", path, w.Code, w.Body.String())
		}
	}
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

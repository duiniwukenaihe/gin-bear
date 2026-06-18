package bear

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func resetTestInjector() {
	injector = &BeanFactory{
		beans: make(map[reflect.Type]any),
	}
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

package bear

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace/noop"
)

type countingReadinessChecker struct {
	name  string
	calls atomic.Int32
	panic any
}

func (c *countingReadinessChecker) Name() string { return c.name }

func (c *countingReadinessChecker) CheckReady(context.Context) error {
	c.calls.Add(1)
	if c.panic != nil {
		panic(c.panic)
	}
	return nil
}

func TestReadinessDeduplicatesSameCheckerInstance(t *testing.T) {
	checker := &countingReadinessChecker{name: "database"}
	results := runReadinessChecks(context.Background(), time.Second, []ReadinessChecker{checker, checker})
	if len(results) != 1 {
		t.Fatalf("results = %v, want one result for one checker instance", results)
	}
	if got := checker.calls.Load(); got != 1 {
		t.Fatalf("checker calls = %d, want 1", got)
	}
}

func TestReadinessPanicBecomesMaskedFailureAndReleasesCoordinator(t *testing.T) {
	const secret = "postgres://admin:secret@database/internal"
	checker := &countingReadinessChecker{name: "database", panic: secret}
	coordinator := newReadinessCheckCoordinator()

	for attempt := 1; attempt <= 2; attempt++ {
		results := runReadinessChecksWithCoordinator(context.Background(), time.Second, []ReadinessChecker{checker}, coordinator)
		if len(results) != 1 || results[0].Err == nil {
			t.Fatalf("attempt %d results = %v, want panic failure", attempt, results)
		}
		if strings.Contains(results[0].Err.Error(), secret) {
			t.Fatalf("attempt %d leaked panic payload: %v", attempt, results[0].Err)
		}
	}
	if got := checker.calls.Load(); got != 2 {
		t.Fatalf("checker calls = %d, want coordinator release after panic", got)
	}
}

func TestCORSMiddlewareUsesOwningRuntimeAcrossConcurrentApplications(t *testing.T) {
	a := newCORSApplication(t, "https://a.example")
	b := newCORSApplication(t, "https://b.example")

	const requests = 32
	var wg sync.WaitGroup
	errs := make(chan string, requests*2)
	for range requests {
		wg.Add(2)
		go func() {
			defer wg.Done()
			assertCORSResponse(a, "https://a.example", errs)
		}()
		go func() {
			defer wg.Done()
			assertCORSResponse(b, "https://b.example", errs)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestCORSMiddlewareBareGinFallbackUsesPublishedConfig(t *testing.T) {
	config := NewSysConfig()
	config.CORS.Enabled = true
	config.CORS.AllowOrigins = []string{"https://fallback.example"}
	Ignite(config)

	engine := gin.New()
	engine.Use(CORSMiddleware())
	engine.GET("/", func(ctx *gin.Context) { ctx.Status(http.StatusNoContent) })
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Origin", "https://fallback.example")
	engine.ServeHTTP(response, request)
	if got := response.Header().Get("Access-Control-Allow-Origin"); got != "https://fallback.example" {
		t.Fatalf("allow origin = %q, want explicit legacy fallback config", got)
	}
}

func newCORSApplication(t *testing.T, origin string) *Bear {
	t.Helper()
	config := NewSysConfig()
	config.CORS.Enabled = true
	config.CORS.AllowOrigins = []string{origin}
	app := Ignite(config)
	app.Use(CORSMiddleware())
	app.GET("/", func(ctx *gin.Context) { ctx.Status(http.StatusNoContent) })
	return app
}

func assertCORSResponse(app *Bear, origin string, errs chan<- string) {
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Origin", origin)
	app.ServeHTTP(response, request)
	if got := response.Header().Get("Access-Control-Allow-Origin"); got != origin {
		errs <- "allow origin = " + got + ", want " + origin
	}
}

func TestMetricsDisabledDoesNotAllocateRuntimeRegistry(t *testing.T) {
	config := NewSysConfig()
	config.Metrics.Enabled = false
	app := Ignite(config)
	if app.Runtime().Metrics != nil {
		t.Fatal("metrics-disabled runtime allocated a metrics registry")
	}
	app.GET("/probe", func(ctx *gin.Context) { ctx.Status(http.StatusNoContent) })
	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/probe", nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("response status = %d, want %d", response.Code, http.StatusNoContent)
	}
}

func TestEnableTracingDoesNotReplaceGlobalProvider(t *testing.T) {
	previous := otel.GetTracerProvider()
	sentinel := noop.NewTracerProvider()
	otel.SetTracerProvider(sentinel)
	t.Cleanup(func() { otel.SetTracerProvider(previous) })

	config := NewSysConfig()
	config.Tracing.Enabled = true
	config.Tracing.Exporter = "none"
	app := Ignite(config)
	app.EnableTracing(context.Background())
	if got := otel.GetTracerProvider(); got != sentinel {
		t.Fatalf("global tracer provider = %T, want sentinel %T", got, sentinel)
	}
}

func TestRuntimeLogsUseStableCodesAndRouteTemplates(t *testing.T) {
	const panicSecret = "database password=hunter2"
	const pathSecret = "customer-token-123"
	app := Ignite(NewSysConfig())
	var output bytes.Buffer
	app.Runtime().Logger = slog.New(slog.NewTextHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}))
	app.GET("/account/:token", func(*gin.Context) { panic(errors.New(panicSecret)) })

	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/account/"+pathSecret, nil))
	logs := output.String()
	if strings.Contains(logs, panicSecret) || strings.Contains(logs, pathSecret) {
		t.Fatalf("runtime logs leaked secret material: %s", logs)
	}
	if !strings.Contains(logs, "BEAR_RUNTIME_PANIC") || !strings.Contains(logs, "/account/:token") {
		t.Fatalf("runtime logs missing stable code or route template: %s", logs)
	}
}

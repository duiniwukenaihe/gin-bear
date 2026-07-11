package bear

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestMetricsAreIsolatedPerApplication(t *testing.T) {
	resetTestInjector()
	resetGinModeForTest(t)

	aConfig := NewSysConfig()
	aConfig.DB.Enabled = false
	aConfig.Metrics.Enabled = true
	a := Ignite(aConfig).EnableMetrics()

	bConfig := NewSysConfig()
	bConfig.DB.Enabled = false
	bConfig.Metrics.Enabled = true
	b := Ignite(bConfig).EnableMetrics()

	performRequest(a, httptest.NewRequest(http.MethodGet, "/missing?token=secret", nil))

	aMetrics := readMetricsBody(t, a)
	bMetrics := readMetricsBody(t, b)

	assertMetricCount(t, aMetrics, "gin_bear_http_requests_total", map[string]string{
		"method": "GET",
		"route":  "unmatched",
		"status": "404",
	}, 1)
	assertMetricCount(t, bMetrics, "gin_bear_http_requests_total", map[string]string{
		"method": "GET",
		"route":  "unmatched",
		"status": "404",
	}, 0)
	for _, want := range []string{"# HELP go_goroutines ", "# HELP process_cpu_seconds_total "} {
		if !strings.Contains(aMetrics, want) {
			t.Fatalf("metrics output missing standard collector %q:\n%s", want, aMetrics)
		}
	}
	if strings.Contains(aMetrics, "secret") || strings.Contains(bMetrics, "secret") {
		t.Fatalf("metrics leaked raw query value:\na=%s\nb=%s", aMetrics, bMetrics)
	}
}

func TestTracingDoesNotRecordRawQuery(t *testing.T) {
	resetGinModeForTest(t)
	oldVersion := Version
	Version = "v9.8.7-test"
	t.Cleanup(func() { Version = oldVersion })

	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() {
		_ = provider.Shutdown(context.Background())
	})

	router := gin.New()
	router.Use(RequestIDMiddleware())
	router.Use(TracingMiddleware(provider, propagation.TraceContext{}))
	router.GET("/users/:id", func(c *gin.Context) {
		c.String(http.StatusAccepted, "ok")
	})

	response := performRequest(router, httptest.NewRequest(http.MethodGet, "/users/42?access_token=secret", nil))
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("span count = %d, spans = %#v", len(spans), spans)
	}
	span := spans[0]
	for _, attr := range span.Attributes {
		if strings.Contains(attr.Value.AsString(), "secret") {
			t.Fatalf("trace leaked query value in %s", attr.Key)
		}
		if string(attr.Key) == "url.query" {
			t.Fatalf("trace recorded raw query attribute: %#v", attr)
		}
		if string(attr.Key) == "url.path" {
			t.Fatalf("trace recorded literal path attribute: %#v", attr)
		}
	}
	if !spanHasStringAttr(span.Attributes, "http.request.method", http.MethodGet) {
		t.Fatalf("missing method attr: %#v", span.Attributes)
	}
	if !spanHasStringAttr(span.Attributes, "http.route", "/users/:id") {
		t.Fatalf("missing route attr: %#v", span.Attributes)
	}
	if !spanHasIntAttr(span.Attributes, "http.response.status_code", http.StatusAccepted) {
		t.Fatalf("missing status attr: %#v", span.Attributes)
	}
	if got := stringAttrValue(span.Attributes, "gin_bear.request_id"); got == "" {
		t.Fatalf("missing generated request id attr: %#v", span.Attributes)
	}
	if !spanHasStringAttr(span.Attributes, "service.version", "v9.8.7-test") {
		t.Fatalf("missing service version attr: %#v", span.Attributes)
	}
}

func TestTracingUsesBoundedUnmatchedRoute(t *testing.T) {
	resetGinModeForTest(t)
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	router := gin.New()
	router.Use(TracingMiddleware(provider, propagation.TraceContext{}))
	rawPath := "/accounts/secret-account/orders/987654"
	response := performRequest(router, httptest.NewRequest(http.MethodGet, rawPath, nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("span count = %d, want 1", len(spans))
	}
	span := spans[0]
	if span.Name != http.MethodGet+" unmatched" {
		t.Fatalf("span name = %q, want bounded unmatched name", span.Name)
	}
	if !spanHasStringAttr(span.Attributes, "http.route", "unmatched") {
		t.Fatalf("route attributes = %#v, want unmatched", span.Attributes)
	}
	for _, attr := range span.Attributes {
		if strings.Contains(attr.Value.AsString(), rawPath) || string(attr.Key) == "url.path" {
			t.Fatalf("trace leaked unmatched literal path in %#v", attr)
		}
	}
}

func TestMetricsNormalizeMethodsToBoundedLabels(t *testing.T) {
	metrics := newHTTPMetricsRegistry(defaultDurationBuckets)
	metrics.Record("get", "/known", http.StatusOK, time.Millisecond)
	metrics.Record("BREW-tenant-123", "/known", http.StatusOK, time.Millisecond)
	metrics.Record("BREW-tenant-456", "/known", http.StatusOK, time.Millisecond)
	body := metrics.RenderPrometheus()

	assertMetricCount(t, body, "gin_bear_http_requests_total", map[string]string{
		"method": "GET", "route": "/known", "status": "200",
	}, 1)
	assertMetricCount(t, body, "gin_bear_http_requests_total", map[string]string{
		"method": "OTHER", "route": "/known", "status": "200",
	}, 2)
	if strings.Contains(body, "BREW-tenant-") || strings.Contains(body, `method="get"`) {
		t.Fatalf("metrics exposed unbounded method labels:\n%s", body)
	}
}

func TestReadinessRunsChecksConcurrentlyAndSanitizesErrors(t *testing.T) {
	resetTestInjector()
	resetGinModeForTest(t)

	cfg := NewSysConfig()
	cfg.DB.Enabled = false
	cfg.Health.ReadinessTimeout = "500ms"
	app := Ignite(cfg)

	var active int32
	var maxActive int32
	app.Beans(
		&databaseReadinessCheck{slowFailingReadinessCheck{name: "database", active: &active, maxActive: &maxActive}},
		&redisReadinessCheck{slowFailingReadinessCheck{name: "redis", active: &active, maxActive: &maxActive}},
	).EnableHealth()
	requireNoError(t, app.ApplyAll(context.Background()))

	start := time.Now()
	response := performRequest(app, httptest.NewRequest(http.MethodGet, "/ready", nil))
	elapsed := time.Since(start)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}
	if elapsed >= 180*time.Millisecond {
		t.Fatalf("readiness checks appear sequential, elapsed=%s body=%s", elapsed, response.Body.String())
	}
	if atomic.LoadInt32(&maxActive) < 2 {
		t.Fatalf("readiness checks did not overlap, max active = %d", maxActive)
	}
	if strings.Contains(response.Body.String(), "password=") {
		t.Fatalf("readiness response leaked dependency error: %s", response.Body.String())
	}

	var body struct {
		Status string            `json:"status"`
		Checks map[string]string `json:"checks"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode readiness body: %v\n%s", err, response.Body.String())
	}
	if body.Status != "not_ready" {
		t.Fatalf("status body = %q, want not_ready", body.Status)
	}
	for _, name := range []string{"database", "redis"} {
		if body.Checks[name] != "failed" {
			t.Fatalf("check %s = %q, want failed in %#v", name, body.Checks[name], body.Checks)
		}
	}
}

func TestRunReadinessChecksReturnsAtDeadlineWhenCheckerIgnoresContext(t *testing.T) {
	release := make(chan struct{})
	defer close(release)

	checkers := []ReadinessChecker{
		&blockingReadinessCheck{name: "z-blocked", release: release},
		&immediateReadinessCheck{name: "a-ready"},
	}
	resultsCh := make(chan []readinessResult, 1)
	start := time.Now()
	go func() {
		resultsCh <- runReadinessChecks(context.Background(), 25*time.Millisecond, checkers)
	}()

	select {
	case results := <-resultsCh:
		if elapsed := time.Since(start); elapsed >= 150*time.Millisecond {
			t.Fatalf("readiness collection exceeded deadline bound: %s", elapsed)
		}
		if len(results) != 2 {
			t.Fatalf("result count = %d, want 2: %#v", len(results), results)
		}
		if results[0].Name != "a-ready" || results[0].Err != nil {
			t.Fatalf("first result = %#v, want successful a-ready", results[0])
		}
		if results[1].Name != "z-blocked" || !errors.Is(results[1].Err, context.DeadlineExceeded) {
			t.Fatalf("second result = %#v, want deadline failure for z-blocked", results[1])
		}
	case <-time.After(150 * time.Millisecond):
		t.Fatal("readiness collection waited for a checker that ignored context")
	}
}

type blockingReadinessCheck struct {
	name    string
	release <-chan struct{}
}

func (c *blockingReadinessCheck) Name() string { return c.name }

func (c *blockingReadinessCheck) CheckReady(context.Context) error {
	<-c.release
	return nil
}

type immediateReadinessCheck struct {
	name string
}

func (c *immediateReadinessCheck) Name() string { return c.name }

func (c *immediateReadinessCheck) CheckReady(context.Context) error { return nil }

type slowFailingReadinessCheck struct {
	name      string
	active    *int32
	maxActive *int32
}

type databaseReadinessCheck struct {
	slowFailingReadinessCheck
}

type redisReadinessCheck struct {
	slowFailingReadinessCheck
}

func (c *slowFailingReadinessCheck) Name() string {
	return c.name
}

func (c *slowFailingReadinessCheck) CheckReady(ctx context.Context) error {
	current := atomic.AddInt32(c.active, 1)
	for {
		seen := atomic.LoadInt32(c.maxActive)
		if current <= seen || atomic.CompareAndSwapInt32(c.maxActive, seen, current) {
			break
		}
	}
	defer atomic.AddInt32(c.active, -1)

	select {
	case <-time.After(100 * time.Millisecond):
		return errors.New(c.name + " unavailable: password=secret")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func readMetricsBody(t *testing.T, app *Bear) string {
	t.Helper()
	response := performRequest(app, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("metrics status = %d body = %s", response.Code, response.Body.String())
	}
	return response.Body.String()
}

func assertMetricCount(t *testing.T, body, name string, labels map[string]string, want float64) {
	t.Helper()
	got, ok := findMetricValue(body, name, labels)
	if !ok {
		got = 0
	}
	if got != want {
		t.Fatalf("%s%v = %v, want %v\n%s", name, labels, got, want, body)
	}
}

func findMetricValue(body, name string, labels map[string]string) (float64, bool) {
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "#") || !strings.HasPrefix(line, name) {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		if !metricLineHasLabels(parts[0], labels) {
			continue
		}
		value, err := strconv.ParseFloat(parts[1], 64)
		if err != nil {
			continue
		}
		return value, true
	}
	return 0, false
}

func metricLineHasLabels(series string, labels map[string]string) bool {
	for key, value := range labels {
		if !strings.Contains(series, key+"=\""+value+"\"") {
			return false
		}
	}
	return true
}

func stringAttrValue(attrs []attribute.KeyValue, key string) string {
	for _, attr := range attrs {
		if string(attr.Key) == key {
			return attr.Value.AsString()
		}
	}
	return ""
}

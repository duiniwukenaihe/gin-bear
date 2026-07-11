package bear

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var defaultDurationBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

var httpMetrics = newHTTPMetricsRegistry(defaultDurationBuckets)

type httpMetricsRegistry struct {
	registry *prometheus.Registry
	requests *prometheus.CounterVec
	errors   *prometheus.CounterVec
	duration *prometheus.HistogramVec
}

func newHTTPMetricsRegistry(buckets []float64) *httpMetricsRegistry {
	copied := append([]float64(nil), buckets...)
	sort.Float64s(copied)

	registry := prometheus.NewRegistry()
	metrics := &httpMetricsRegistry{
		registry: registry,
		requests: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "gin_bear_http_requests_total",
				Help: "Total HTTP requests handled by gin-bear.",
			},
			[]string{"method", "route", "status"},
		),
		errors: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "gin_bear_http_errors_total",
				Help: "Total HTTP requests with status code >= 400.",
			},
			[]string{"method", "route", "status"},
		),
		duration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "gin_bear_http_request_duration_seconds",
				Help:    "HTTP request duration in seconds.",
				Buckets: copied,
			},
			[]string{"method", "route", "status"},
		),
	}
	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		metrics.requests,
		metrics.errors,
		metrics.duration,
	)
	return metrics
}

func recordHTTPRequestMetric(ctx *gin.Context, latency time.Duration) {
	route := ctx.FullPath()
	if route == "" {
		route = "unmatched"
	}
	httpMetrics.Record(ctx.Request.Method, route, ctx.Writer.Status(), latency)
}

func (r *httpMetricsRegistry) Record(method, route string, status int, latency time.Duration) {
	if r == nil {
		return
	}
	method = normalizeHTTPMethod(method)
	statusLabel := strconv.Itoa(status)
	r.requests.WithLabelValues(method, route, statusLabel).Inc()
	if status >= http.StatusBadRequest {
		r.errors.WithLabelValues(method, route, statusLabel).Inc()
	}
	r.duration.WithLabelValues(method, route, statusLabel).Observe(latency.Seconds())
}

func normalizeHTTPMethod(method string) string {
	switch strings.ToUpper(strings.TrimSpace(method)) {
	case http.MethodConnect:
		return http.MethodConnect
	case http.MethodDelete:
		return http.MethodDelete
	case http.MethodGet:
		return http.MethodGet
	case http.MethodHead:
		return http.MethodHead
	case http.MethodOptions:
		return http.MethodOptions
	case http.MethodPatch:
		return http.MethodPatch
	case http.MethodPost:
		return http.MethodPost
	case http.MethodPut:
		return http.MethodPut
	case http.MethodTrace:
		return http.MethodTrace
	default:
		return "OTHER"
	}
}

func (r *httpMetricsRegistry) RenderPrometheus() string {
	if r == nil {
		return ""
	}
	writer := &metricsResponseWriter{header: make(http.Header)}
	request, err := http.NewRequest(http.MethodGet, "/metrics", nil)
	if err != nil {
		return ""
	}
	r.Handler().ServeHTTP(writer, request)
	return writer.body.String()
}

func (r *httpMetricsRegistry) Handler() http.Handler {
	if r == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		})
	}
	return promhttp.HandlerFor(r.registry, promhttp.HandlerOpts{})
}

type metricsResponseWriter struct {
	header http.Header
	body   strings.Builder
	status int
}

func (w *metricsResponseWriter) Header() http.Header {
	return w.header
}

func (w *metricsResponseWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(body)
}

func (w *metricsResponseWriter) WriteHeader(status int) {
	w.status = status
}

func resetMetricsForTest() {
	httpMetrics = newHTTPMetricsRegistry(defaultDurationBuckets)
}

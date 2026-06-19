package bear

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

var defaultDurationBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

var httpMetrics = newHTTPMetricsRegistry(defaultDurationBuckets)

type httpMetricKey struct {
	Method string
	Route  string
	Status int
}

type httpMetricSample struct {
	Requests int64
	Errors   int64
	Sum      float64
	Count    int64
	Buckets  []int64
}

type httpMetricsRegistry struct {
	mu      sync.RWMutex
	buckets []float64
	samples map[httpMetricKey]*httpMetricSample
}

func newHTTPMetricsRegistry(buckets []float64) *httpMetricsRegistry {
	copied := append([]float64(nil), buckets...)
	sort.Float64s(copied)
	return &httpMetricsRegistry{
		buckets: copied,
		samples: make(map[httpMetricKey]*httpMetricSample),
	}
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
	r.mu.Lock()
	defer r.mu.Unlock()

	key := httpMetricKey{Method: method, Route: route, Status: status}
	sample := r.samples[key]
	if sample == nil {
		sample = &httpMetricSample{Buckets: make([]int64, len(r.buckets))}
		r.samples[key] = sample
	}
	sample.Requests++
	if status >= http.StatusBadRequest {
		sample.Errors++
	}
	seconds := latency.Seconds()
	sample.Sum += seconds
	sample.Count++
	for i, bucket := range r.buckets {
		if seconds <= bucket {
			sample.Buckets[i]++
		}
	}
}

func (r *httpMetricsRegistry) RenderPrometheus() string {
	if r == nil {
		return ""
	}
	r.mu.RLock()
	defer r.mu.RUnlock()

	var b strings.Builder
	b.WriteString("# HELP gin_bear_http_requests_total Total HTTP requests handled by gin-bear.\n")
	b.WriteString("# TYPE gin_bear_http_requests_total counter\n")
	for _, key := range r.sortedKeys() {
		sample := r.samples[key]
		fmt.Fprintf(&b, "gin_bear_http_requests_total%s %d\n", prometheusLabels(key, ""), sample.Requests)
	}

	b.WriteString("# HELP gin_bear_http_errors_total Total HTTP requests with status code >= 400.\n")
	b.WriteString("# TYPE gin_bear_http_errors_total counter\n")
	for _, key := range r.sortedKeys() {
		sample := r.samples[key]
		if sample.Errors == 0 {
			continue
		}
		fmt.Fprintf(&b, "gin_bear_http_errors_total%s %d\n", prometheusLabels(key, ""), sample.Errors)
	}

	b.WriteString("# HELP gin_bear_http_request_duration_seconds HTTP request duration in seconds.\n")
	b.WriteString("# TYPE gin_bear_http_request_duration_seconds histogram\n")
	for _, key := range r.sortedKeys() {
		sample := r.samples[key]
		for i, bucket := range r.buckets {
			fmt.Fprintf(&b, "gin_bear_http_request_duration_seconds_bucket%s %d\n", prometheusLabels(key, strconv.FormatFloat(bucket, 'f', -1, 64)), sample.Buckets[i])
		}
		fmt.Fprintf(&b, "gin_bear_http_request_duration_seconds_bucket%s %d\n", prometheusLabels(key, "+Inf"), sample.Count)
		fmt.Fprintf(&b, "gin_bear_http_request_duration_seconds_sum%s %.9f\n", prometheusLabels(key, ""), sample.Sum)
		fmt.Fprintf(&b, "gin_bear_http_request_duration_seconds_count%s %d\n", prometheusLabels(key, ""), sample.Count)
	}

	return b.String()
}

func (r *httpMetricsRegistry) sortedKeys() []httpMetricKey {
	keys := make([]httpMetricKey, 0, len(r.samples))
	for key := range r.samples {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Method != keys[j].Method {
			return keys[i].Method < keys[j].Method
		}
		if keys[i].Route != keys[j].Route {
			return keys[i].Route < keys[j].Route
		}
		return keys[i].Status < keys[j].Status
	})
	return keys
}

func prometheusLabels(key httpMetricKey, le string) string {
	labels := []string{
		`method="` + escapePrometheusLabel(key.Method) + `"`,
		`route="` + escapePrometheusLabel(key.Route) + `"`,
		`status="` + strconv.Itoa(key.Status) + `"`,
	}
	if le != "" {
		labels = append(labels, `le="`+escapePrometheusLabel(le)+`"`)
	}
	return "{" + strings.Join(labels, ",") + "}"
}

func escapePrometheusLabel(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, "\n", `\n`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return value
}

func resetMetricsForTest() {
	httpMetrics = newHTTPMetricsRegistry(defaultDurationBuckets)
}

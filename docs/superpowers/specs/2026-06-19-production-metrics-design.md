# Production Metrics Design

**Goal:** Add a built-in Prometheus text metrics endpoint so generated services have basic production observability without extra dependencies.

**Scope:** This round covers HTTP request metrics only. Distributed tracing, OpenTelemetry exporters, process/runtime metrics, dashboards, and alert rules remain follow-up work.

## Approach

The framework already has `MetricsConfig` and request performance middleware. We will connect those pieces with a small in-process metrics registry:

- `PerformanceMiddleware` records each completed request.
- Metrics are labeled by HTTP method, route pattern, and status code.
- Histogram buckets track request duration in seconds.
- `EnableMetrics` registers the configured metrics path, defaulting to `/metrics`.
- `EnableHealth` also enables metrics when `metrics.enabled` is true, because production public paths and examples already expose `/metrics`.
- The endpoint emits Prometheus text exposition format using standard Go only.

Route labels use `gin.Context.FullPath()` to avoid raw-path high cardinality. Requests without a matched route use `unmatched`.

## Metrics

- `gin_bear_http_requests_total{method,route,status}`
- `gin_bear_http_errors_total{method,route,status}`
- `gin_bear_http_request_duration_seconds_bucket{method,route,status,le}`
- `gin_bear_http_request_duration_seconds_sum{method,route,status}`
- `gin_bear_http_request_duration_seconds_count{method,route,status}`

## Testing

Tests should reset the in-memory registry, issue successful and failing requests, scrape `/metrics`, and assert stable Prometheus output. The registry must be concurrency-safe for race tests.

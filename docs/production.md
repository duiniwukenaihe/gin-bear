# Production Guide

## Runtime

Use production mode in deployed environments:

```bash
export BEAR_ENV=prod
export GIN_MODE=release
export JWT_SECRET="$(openssl rand -base64 48)"
```

`server.mode: release` in YAML has the same effect as `GIN_MODE=release`.

## Configuration

Start from `application-prod.yaml.example` and keep real secrets outside git. Supported environment overrides include:

- `BEAR_SERVER_PORT`
- `JWT_SECRET`
- `REDIS_ADDR`
- `POSTGRES_HOST`
- `POSTGRES_PORT`
- `POSTGRES_USER`
- `POSTGRES_PASSWORD`
- `POSTGRES_DB`

## Health Checks

- `/live` confirms the process is alive.
- `/ready` confirms registered dependencies are ready.
- `/health` remains as a backward-compatible liveness alias.
- `/version` exposes build metadata for the running binary.

Use `/ready` for load balancer readiness and rollout gates.

## Version Metadata

Inject release identity at build time:

```bash
VERSION=v1.2.3
COMMIT="$(git rev-parse --short HEAD)"
BUILD_TIME="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

go build \
  -ldflags="-X github.com/duiniwukenaihe/gin-bear/pkg/bear.Version=${VERSION} -X github.com/duiniwukenaihe/gin-bear/pkg/bear.Commit=${COMMIT} -X github.com/duiniwukenaihe/gin-bear/pkg/bear.BuildTime=${BUILD_TIME}" \
  ./cmd
```

For Docker builds:

```bash
docker build \
  --build-arg VERSION="${VERSION}" \
  --build-arg COMMIT="${COMMIT}" \
  --build-arg BUILD_TIME="${BUILD_TIME}" \
  -t gin-bear:${VERSION} .
```

The `/version` endpoint returns `version`, `commit`, `build_time`, `go_version`, `os`, and `arch`.

When `bear-cli new` clones this repository as a full application template, generated build artifacts rewrite the `-X` package path to the new module, for example `my-app/pkg/bear.Version`. This applies to the Dockerfile and the copied GitHub Actions workflow. The legacy `bear new` command generates a lightweight app that imports the upstream framework package, so its Dockerfile intentionally keeps `github.com/duiniwukenaihe/gin-bear/pkg/bear`.

## Metrics

Enable the built-in Prometheus text endpoint with:

```yaml
metrics:
  enabled: true
  path: "/metrics"
```

`EnableHealth()` registers `/metrics` when metrics are enabled. The endpoint exposes:

- `gin_bear_http_requests_total`
- `gin_bear_http_errors_total`
- `gin_bear_http_request_duration_seconds_bucket`
- `gin_bear_http_request_duration_seconds_sum`
- `gin_bear_http_request_duration_seconds_count`

Request metrics are labeled by `method`, Gin route pattern, and `status`.

## Tracing

Enable OpenTelemetry tracing explicitly:

```yaml
tracing:
  enabled: true
  service_name: "gin-bear"
  exporter: "otlp"
  otlp_endpoint: "http://otel-collector:4318/v1/traces"
  sample_rate: 1.0
```

Call `app.EnableTracing(ctx)` during startup. The HTTP middleware extracts W3C `traceparent` headers, creates server spans named like `GET /users/:id`, and records method, route, status, client address, request id, and Gin errors. Supported exporters are `stdout`, `otlp`, and `none`.

## OpenAPI And Swagger

Enable Swagger/OpenAPI with:

```go
app.EnableSwagger()
```

The JSON document is served at `/swagger/doc.json`, and the built-in Swagger UI entry is `/swagger`.

The generator uses route metadata and handler function signatures to infer:

- OpenAPI path syntax, for example `/users/:id` becomes `/users/{id}`.
- `uri` tags as path parameters.
- `form` and `query` tags as query parameters.
- `json` tags as request body fields.
- the first non-error handler return value as the `200` response schema.

When auth config is present, the document includes a JWT bearer `BearerAuth` security scheme and top-level security requirement.

This is a best-effort contract generator. For externally consumed APIs, review the generated document in code review and add explicit documentation around business errors, public-route exceptions, pagination, and non-200 responses.

## Database Migrations

Do not run implicit schema migrations from request-serving code. Keep schema changes explicit and reviewed:

1. Generate SQL migrations as reviewed files.
2. Review migration SQL in code review.
3. Run migrations as a separate deploy step before starting the new app version.
4. Keep application startup limited to connection and readiness checks.

Migration files can use this naming convention:

```text
migrations/
  001_create_users.up.sql
  001_create_users.down.sql
  002_add_user_email.up.sql
```

Run them explicitly from a deploy command or one-off admin tool:

```go
adapter, err := bear.NewGormAdapter(cfg.DB)
if err != nil {
    return err
}
sqlDB, err := adapter.DB.DB()
if err != nil {
    return err
}
migrations, err := bear.LoadSQLMigrations("migrations")
if err != nil {
    return err
}
runner := bear.NewMigrationRunner(sqlDB)
return runner.Up(context.Background(), migrations)
```

Applied versions are recorded in `schema_migrations`, and rerunning `Up` skips versions that have already been applied.

`Up` and `Down` acquire a portable migration lock so concurrent runners do not apply or roll back schema changes at the same time. Roll back the latest applied migration steps with:

```go
return runner.Down(context.Background(), migrations, 1)
```

`database.slow_query_threshold` enables GORM slow query logging for runtime visibility.

## Request Binding

Handler request structs can safely combine path, query, form, and JSON body tags:

```go
type UpdateUserRequest struct {
    ID   int64  `uri:"id" binding:"required"`
    Page int    `form:"page"`
    Name string `json:"name" binding:"required"`
}
```

The framework binds URI values first, query/form values second, JSON or form body values third, and then runs validation once.

## WebSocket Origin Policy

Keep `websocket.check_origin: true` in production. Use `websocket.allowed_origins` to list trusted browser origins:

```yaml
websocket:
  check_origin: true
  allowed_origins:
    - "https://example.com"
```

Production startup rejects `check_origin: false` unless an explicit allowlist is configured.

## Rate Limiting

`NewRedisRateLimiter` remains fail-open by default for backward compatibility. Sensitive endpoints can deny requests when Redis is unavailable:

```go
limiter := bear.NewRedisRateLimiter(redisAdapter, 100, time.Minute)
limiter.FailClosed = true
app.Use(bear.RateLimitMiddleware(limiter))
```

## Dynamic Plugins

Dynamic `.so` plugins are disabled by default. Enable them only when the deployment controls the plugin directory:

```yaml
plugins:
  enabled: true
  allowed_dirs:
    - "/app/plugins"
```

Plugin paths are resolved to absolute paths and must live inside one of the configured directories.

## Delivery Checks

Run the same core checks locally before cutting a release:

```bash
GOPROXY=https://goproxy.cn,direct scripts/release-check.sh
docker build .
```

`scripts/release-check.sh` installs `govulncheck` when needed and generates `sbom.spdx.json` when `syft` is available.

## Containers

Build the app image:

```bash
docker build -t gin-bear .
```

Run local dependencies:

```bash
docker compose up --build
```

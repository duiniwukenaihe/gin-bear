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

Use `/ready` for load balancer readiness and rollout gates.

## Database Migrations

Do not run implicit schema migrations from request-serving code. Keep schema changes explicit and reviewed:

1. Generate SQL migrations with your migration tool of choice.
2. Review migration SQL in code review.
3. Run migrations as a separate deploy step before starting the new app version.
4. Keep application startup limited to connection and readiness checks.

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
GOPROXY=https://goproxy.cn,direct go mod tidy
git diff --exit-code -- go.mod go.sum
GOPROXY=https://goproxy.cn,direct go build ./cmd ./cmd/bear ./cmd/bear-cli
GOPROXY=https://goproxy.cn,direct go test ./... -count=1
GOPROXY=https://goproxy.cn,direct go test -race ./... -count=1
GOPROXY=https://goproxy.cn,direct go vet ./...
GOPROXY=https://goproxy.cn,direct govulncheck ./...
docker build .
```

Install `govulncheck` when it is not already available:

```bash
GOPROXY=https://goproxy.cn,direct go install golang.org/x/vuln/cmd/govulncheck@latest
```

## Containers

Build the app image:

```bash
docker build -t gin-bear .
```

Run local dependencies:

```bash
docker compose up --build
```

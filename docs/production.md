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
- `DB_MAX_OPEN_CONNS`
- `DB_MAX_IDLE_CONNS`
- `LOG_LEVEL`
- `BEAR_SHUTDOWN_TIMEOUT`
- `BEAR_READINESS_TIMEOUT`
- `METRICS_PATH`
- `TRACING_EXPORTER`
- `TRACING_OTLP_ENDPOINT`
- `REDIS_REQUIRED`

Production configuration is always strict. `LoadConfig(paths ...string)` loads
files in order, returns syntax, unknown-field, and validation errors, then
applies environment overrides. YAML uses known-field validation and JSON
rejects unknown fields. Development remains strict by default, but can
temporarily accept legacy extension keys with:

```yaml
config:
  strict: false
```

Production rejects `config.strict: false`. `InitConfig()` remains available for
source compatibility and delegates to `LoadConfig`; because its signature has
no error result, it panics on any loading or validation error.

The tested production-loading pattern is in
[`examples/migration/main.go`](../examples/migration/main.go). It calls
`LoadConfig` and returns startup errors to the caller instead of relying on the
legacy panic behavior.

## HTTP Security Defaults

Request headers and bodies default to 1 MiB limits. Tune either bound only for
an endpoint profile that requires it:

```yaml
server:
  max_header_bytes: 1048576
  max_request_body_bytes: 1048576
  trusted_proxies:
    - "10.0.0.0/8"
```

When `trusted_proxies` is omitted or empty, Gin trusts no proxy and ignores
forwarded client IP headers. Configure only the CIDRs of load balancers or
reverse proxies that connect directly to the application.

Client `X-Request-ID` values are accepted only when they match
`[A-Za-z0-9._-]{1,128}`; all other values are replaced. Responses include
`X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, and
`Referrer-Policy: no-referrer`. The framework intentionally does not emit HSTS
because HTTPS and HSTS policy usually belong to the TLS termination layer.

CORS startup validation rejects `allow_origins: ["*"]` when
`allow_credentials: true`. Use an explicit origin allowlist for credentialed
browser requests.

## JWT Authentication

JWT parsing accepts HS256 only. Issuer, audience, and clock skew checks are
optional; configured clock skew must be between zero and five minutes:

```yaml
auth:
  jwt_secret: "set-with-JWT_SECRET-32-plus-random-chars"
  jwt_issuer: "https://auth.example.com"
  jwt_audience: "gin-bear-api"
  jwt_clock_skew: "30s"
```

Set the actual secret with `JWT_SECRET`; the example value is deliberately
recognized as unsafe and production startup rejects it. Configure matching
`Issuer`, `Audience`, and `ClockSkew` fields on `JWTUtil.Config` when creating
the JWT utility. Generated tokens include configured issuer and audience
claims, and parsing requires them when configured.

Redis-backed token revocation is optional. Without a Redis client, ordinary JWT
validation still succeeds. `RevokeToken` and direct blacklist checks return
`ErrTokenRevocationUnavailable`, which callers can detect with `errors.Is`,
instead of panicking. Treat that error as a failed logout/revocation operation.
The tested typed-error handling pattern is in
[`examples/auth/main.go`](../examples/auth/main.go).

## Logging

Configure structured JSON log verbosity with:

```yaml
log:
  level: "info" # debug, info, warn, error
```

Use `log.level` for file-based configuration and `LOG_LEVEL=debug` locally when diagnosing framework binding, routing, or dependency behavior.

## Health Checks

- `/live` confirms the process is alive.
- `/ready` confirms registered dependencies are ready.
- `/health` remains as a backward-compatible liveness alias.
- `/version` exposes build metadata for the running binary.

Use `/ready` for load balancer readiness and rollout gates.
Readiness responses expose only `ok` or `failed` per dependency; detailed
causes are kept in structured logs. Checks run concurrently, each with the
configured readiness timeout.

Tune readiness checks and graceful shutdown for the service profile:

```yaml
server:
  shutdown_timeout: "10s"

health:
  readiness_timeout: "3s"
```

The equivalent configuration keys are `server.shutdown_timeout` and `health.readiness_timeout`.

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

The `/version` endpoint returns `version`, `commit`, `build_time`, `go_version`, `os`, and `arch`.

When building a generated application, target the generated module path in linker flags, for example `my-app/pkg/bear.Version`.

## Metrics

Production examples keep metrics disabled until explicitly enabled. Enable the
built-in Prometheus endpoint with:

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
Each `Bear` runtime owns an isolated Prometheus registry with Go and process
collectors, so tests and colocated runtimes do not share HTTP metric state.
`/metrics` is no longer in the default authentication public-path list. Expose
it through a separately protected listener, network policy, or an explicit
`auth.public_paths` entry only when that is intentional.

## Tested Startup Example

[`examples/basic/main.go`](../examples/basic/main.go) is the source behind the
README lifecycle guidance. It checks the error returned by `ApplyAll`, starts
with a signal-cancellable context, and returns any `Launch` error to `main`.
Its route setup is exercised by `examples/basic/main_test.go` as part of
`go test ./...`.

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

Call `app.EnableTracing(ctx)` during startup. The HTTP middleware extracts W3C `traceparent` headers, creates server spans named like `GET /users/:id`, and records method, route, status, client address, generated request id, service version, and Gin errors. Raw query strings are not recorded. Supported exporters are `stdout`, `otlp`, and `none`.

## OpenAPI And Swagger

Enable Swagger/OpenAPI with:

```go
app.EnableSwagger()
```

The JSON document is served at `/swagger/doc.json`, and the built-in Swagger
UI entry is `/swagger`. Swagger is not registered in production mode. The
development UI uses an immutable Swagger UI version with SRI and a restrictive
Content Security Policy.

The generator uses route metadata and handler function signatures to infer:

- OpenAPI path syntax, for example `/users/:id` becomes `/users/{id}`.
- `uri` tags as path parameters.
- `form` and `query` tags as query parameters.
- `json` tags as request body fields.
- the first non-error handler return value as the `200` response schema.

Auth configuration alone never declares OpenAPI security. A globally attached
`AuthFairing` adds the JWT bearer `BearerAuth` scheme and top-level security
requirement. Route or controller authentication is declared only from that
route's effective fairing metadata. Public paths explicitly override an actual
authentication requirement with an empty requirement. Generated operations
include standard JSON error responses for `400`, `403`, `404`, and `500`;
authenticated private routes also include `401`.

`Handle`, `HandleWithFairing`, and mounted controller registration automatically
write private per-`Bear` metadata containing the final full path, concrete
`IOpenAPI` controller instance, and combined route/controller fairings.
`RouteMetadata` retains its original five-field public shape. Missing private
identity is handled conservatively: the generator does not search unrelated
controller beans by relative path and does not guess controller metadata or
interceptor authentication.

Generation fails on invalid route metadata, duplicate method/path entries, and
duplicate explicit operation IDs. For externally consumed APIs, review the
generated document in code review and add explicit documentation around
business errors and pagination.

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
runner := bear.NewMigrationRunnerWithDialect(sqlDB, bear.MigrationDialectPostgreSQL)
return runner.Up(context.Background(), migrations)
```

Configure deployment-specific history and lock tables on the explicit runner:

```go
runner := bear.NewMigrationRunnerWithDialect(sqlDB, bear.MigrationDialectPostgreSQL).
    ConfigureTables("tenant_schema_migrations", "tenant_schema_migration_locks")
```

Use `MigrationDialectSQLite`, `MigrationDialectMySQL`, or
`MigrationDialectPostgreSQL` for an explicit deployment contract. The explicit
constructor returns an independent dialect runner; creating another runner for
the same `*sql.DB` cannot overwrite it. The legacy `NewMigrationRunner(sqlDB)`
constructor and direct `MigrationRunner` literals remain source-compatible and
infer SQLite, MySQL, and PostgreSQL from known driver types on each operation.
Unknown or wrapped drivers fail closed with guidance to use
`NewMigrationRunnerWithDialect`; they are never silently treated as SQLite.

Applied versions are recorded in `schema_migrations`, and rerunning `Up` skips
versions that have already been applied. SQLite and PostgreSQL execute each
migration and its history update in one transaction by default. MySQL DDL is
not transactional, so the runner first persists a `dirty` history row, executes
the SQL, and clears the dirty flag only after the final history update succeeds.
`Up` and `Down` refuse to continue while any dirty version exists.

PostgreSQL statements that cannot run in a transaction, such as `CREATE INDEX
CONCURRENTLY` and `DROP INDEX CONCURRENTLY`, must opt in per direction. Put this
exact directive on the first non-empty line of the corresponding `.up.sql` or
`.down.sql` file:

```sql
-- gin-bear:non-transactional
CREATE INDEX CONCURRENTLY users_email_idx ON users (email);
```

The runner does not infer this mode from SQL text. Marked PostgreSQL directions
use the same dirty-state protocol as MySQL.

After a MySQL or marked PostgreSQL `Up` failure, inspect the actual schema before
resolving the state. Recovery is intentionally available only on the explicit
dialect runner. If `Up` completed, keep the history row and mark it applied:

```go
return runner.ForceMigrationState(ctx, "002", true)
```

If `Up` did not complete and is safe to run again, remove the dirty history row:

```go
return runner.ForceMigrationState(ctx, "002", false)
```

For non-transactional `Down`, keep the dirty row until the schema has been
inspected. A driver may report an error after one or more statements in a
multi-statement `Down` have already executed, so an SQL error does not prove
that the migration is still fully applied or fully rolled back. After inspection,
use `ForceMigrationState(ctx, version, true)` if the schema matches the applied
state, or `ForceMigrationState(ctx, version, false)` if it matches the rolled-back
state. Repair a partially rolled-back schema manually before choosing either
state.

If all `Down` SQL succeeds but deleting the history row fails, the migration is
no longer applied; after confirming the schema, remove the dirty row with
`ForceMigrationState(ctx, version, false)`.

This force operation never executes migration SQL and never decides which
state the database reached. That decision belongs to the operator. Legacy
history tables are upgraded with `dirty = false`; legacy lock tables receive
an owner column.

`Up` and `Down` acquire a portable migration lock so concurrent runners do not apply or roll back schema changes at the same time. Roll back the latest applied migration steps with:

```go
return runner.Down(context.Background(), migrations, 1)
```

`database.slow_query_threshold` enables GORM slow query logging for runtime visibility.

## Redis Dependency Mode

Redis remains optional by default. When a service requires Redis for authentication state, distributed rate limiting, or core business behavior, fail fast at startup:

```yaml
redis:
  addr: "redis:6379"
  required: true
```

Use `redis.required` for file-based configuration. `REDIS_REQUIRED=true` applies the same behavior through environment configuration.

## Code Generation

`bear gen api` generates CRUD packages with DTO-to-model mapping, pointer-based update DTOs, bounded pagination, and safe package names for dashed or underscored resources:

```bash
bear gen api user-profile --fields "name:string,email:email,age:int,birthday:datetime,bio:text"
```

Generated query DTOs default to page `1`, page size `20`, and cap page size at `100`.

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

For v0.10, dynamic plugins are a startup-only, experimental integration point.
Load every required plugin before `ApplyAll`. Once startup begins, `LoadPlugin` and
`ReloadPlugin` return `bear.ErrPluginHotReloadUnsupported`; Go plugins cannot
safely replace lifecycle-owned resources in a live process. Publish the new
plugin with a new application instance and use a readiness-checked rolling
replacement to move traffic.

`Launch` closes the same registration barrier even when an application skips
`ApplyAll`. Register lifecycle components and shutdown hooks before startup:
`Lifecycle.Add` and `Bear.OnShutdown` return
`bear.ErrLifecycleRegistrationClosed` once startup has begun. Shutdown remains
strictly LIFO; when its context expires, Bear does not start another component.
A legacy `Shutdown()` call that already started may finish in one bounded
background worker, while `Stop` returns the context error and caches that result.

## Delivery Checks

Run the project verification gate locally before cutting a release:

```bash
GOSUMDB=sum.golang.org GOTOOLCHAIN=go1.25.12 make verify
```

This is the same pinned verification command used by the release workflow.

# Gin-Bear Production Baseline Design

Date: 2026-06-18
Scope: P0 production hardening for the gin-bear framework core.

## Goal

Move gin-bear from a development-oriented scaffold toward a production-safe baseline without changing the public programming model, generated project layout, or CLI workflow.

This pass focuses on default behavior that can cause runtime risk in production: startup ordering, HTTP server timeouts, CORS policy, optional database usage, and error response leakage.

## Non-Goals

- No large directory restructure.
- No new application template architecture.
- No new auth provider, migration engine, tracing stack, or deployment platform integration.
- No changes to controller handler signatures unless required to fix unsafe behavior.

## Approach

Use a small compatibility-preserving patch in `pkg/bear`.

1. Load and register configuration before config-dependent middleware is constructed.
2. Add server timeout settings to `SysConfig` and use them when constructing `http.Server`.
3. Replace hard-coded permissive CORS behavior with configuration-driven defaults.
4. Treat the database as optional unless `database.enabled=true`.
5. Return safe client-facing errors while logging full internal details with request context.

## Components

### Configuration

Add production defaults while preserving existing YAML compatibility:

- `server.read_header_timeout`
- `server.read_timeout`
- `server.write_timeout`
- `server.idle_timeout`
- `server.max_header_bytes`
- `cors.enabled`
- `cors.allow_origins`
- `cors.allow_methods`
- `cors.allow_headers`
- `cors.allow_credentials`
- `cors.max_age`

Duration fields will use strings such as `5s` and `1m` for readability. Defaults are conservative and suitable for regular JSON APIs.

### Startup

`Ignite` will:

1. Build the `Bear` engine.
2. Load or post-process config.
3. Validate production-critical config.
4. Initialize logger and register `Bear` and `SysConfig`.
5. Configure Gin mode and trusted proxies when provided.
6. Register base middleware.

This makes middleware configuration deterministic.

### HTTP Server

`Launch` will construct `http.Server` with configured timeout values. Defaults:

- `ReadHeaderTimeout`: `5s`
- `ReadTimeout`: `15s`
- `WriteTimeout`: `30s`
- `IdleTimeout`: `60s`
- `MaxHeaderBytes`: Go default when unset

Shutdown remains graceful with the existing 5 second timeout.

### CORS

The existing `CORSMiddleware()` will remain callable, but will use config. Defaults:

- disabled unless explicitly enabled
- no credentials by default
- allowed origins must be configured for browser credentialed requests

If CORS is enabled without origins, use no permissive wildcard when credentials are enabled.

### Database Optionality

`database.enabled=false` means the framework can start without database credentials. DB validation remains strict when:

- `database.enabled=true`
- a caller explicitly constructs `NewGormAdapter`

This supports services that only expose health, proxy, memory-only, CLI, or plugin functionality.

### Error Responses

`handleError` will keep full errors in structured logs but return safe messages:

- `BearError`: use its message/key and status mapping.
- unknown errors: HTTP 500 with `Internal server error` and request id when present.
- validation/binding errors: HTTP 400 with a stable `Invalid request` message.

This avoids leaking SQL, Redis, file paths, stack details, and validator internals.

## Testing

Tests should be added before production changes:

1. `Ignite` with `database.enabled=false` succeeds without DB name or DSN.
2. `PerformanceMiddleware` sees configured log/slow request values after startup ordering changes.
3. `buildHTTPServer` or equivalent helper applies configured timeouts.
4. CORS enabled with explicit origin returns configured headers.
5. Unknown handler errors return safe client JSON while preserving 500 status.
6. Validation errors return stable 400 messages without raw validator internals.

If dependency download fails due network timeouts, record the failure and rerun once modules are available.

## Rollout

This is a backwards-compatible framework hardening pass. Existing applications can keep using `bear.Ignite()`, `app.Mount`, `app.ApplyAll`, and `app.Launch`.

Projects that depended on permissive global CORS should explicitly configure CORS. Projects that depended on startup failure without DB credentials should set `database.enabled=true`.

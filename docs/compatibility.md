# Compatibility Contract

## v0 Public Surface

The v0 public API remains available, including `Ignite`, `GetInjector`,
`GetByType`, `Handle`, `Mount`, `ApplyAll`, `Launch`, the existing config
structs, and existing adapter constructors. Existing YAML keys remain accepted,
including compatibility-only keys, so an existing application can continue to
compile and load its configuration while it plans a migration.

## Feature States

See [supported features](supported-features.md) for the supported,
experimental, and compatibility-only categories. Compatibility-only APIs are
annotated with Go `Deprecated:` comments. They are retained for source and
configuration compatibility, not for new feature work.

## Compatibility-only Configuration

`Ignite` logs one structured warning for every enabled compatibility-only
configuration that is currently a no-op. The warning keys are `waf`, `geoip`,
`bigquery`, `mq`, `kafka`, `rocketmq`, `pulsar`, `schema`,
`circuit_breaker`, and `config_center`. Warnings are deduplicated within each
startup.

The ID generator is retained as a deprecated method without an enabled config
flag. The legacy `GRPCService` interface remains for source compatibility; new
gRPC services use the supported optional `GRPCServiceRegistrar` and
error-returning registration APIs.

## Runtime Modes And Additive APIs

When the extension keys are absent, the compatibility defaults are
`framework.strict: false` and `framework.response_mode: raw`. Compatibility
mode preserves the historical Fairing order, IoC failure timing, lifecycle
retry behavior, build/init ordering, and bare handler responses. Applications
opt into strict runtime checks with `framework.strict: true` and opt into
automatic response envelopes independently with
`framework.response_mode: envelope`.

`framework.strict` is not the same setting as `config.strict`. The latter
controls unknown configuration fields and is forced on in production; the
former controls strict framework runtime behavior and remains opt-in for an
existing application. Production rejects compatibility runtime mode unless the
migration-only `framework.allow_compatibility_in_production: true` override is
explicitly configured; new scaffolds set it to `false`.

`IgniteE` is the error-returning startup alternative to `Ignite`; the legacy
API remains a panic-compatible wrapper. `Serve` is the signal-free,
single-owner serving API; `Launch` remains the signal-aware compatibility
wrapper. Existing public signatures remain available, while strict migrations
can use the additive error-returning registration and resolution APIs.
After a successful serving lifecycle, the Bear instance is not restartable;
create a new instance for a replacement process. An established strict Gin
mode also prevents compatibility instances from changing that process-global
mode.

Strict registration errors are available from the additive `...E` APIs. In
strict mode, Bear-owned legacy fluent registration wrappers call those APIs and
panic when the registration is invalid, preserving their original signatures
without silently discarding the failure. The promoted Gin `Use` method remains
unchanged for v0 compatibility; strict applications should use `UseE`.

Bear still embeds a public `*gin.Engine`, and `GroupE` still returns a raw
`*gin.RouterGroup` for v0 source compatibility. Strict sealing therefore covers
Bear-managed registration APIs, not arbitrary direct Gin mutation. Complete all
direct Gin setup before startup and do not register modules, routes, or groups
concurrently. Hiding the engine and replacing raw groups with an owned
registration context is reserved for v0.10 because it would break existing
applications.

`auth.enabled` is additive and defaults to `false`. Setting it to `true`
requests automatic global HTTP authentication. Existing applications that
manually attach `AuthFairing` remain supported, and production secret validation
still applies to that path. The setting does not authenticate gRPC; gRPC policy
belongs in unary and stream interceptors.

`auth.storage_type` accepts `jwt` or `redis`. The legacy `file` value maps to
stateless JWT validation with a startup warning.

`OpenAPIConfig.Apps`, `TimeWindow`, `ReplayCheck`, and `HeaderPrefix` remain
accepted for v0 configuration and source compatibility but are deprecated.
They do not implement request signing or authentication.

Development builds of `cmd/bear` do not guess a framework version. Use
`--framework-version dev --framework-replace /absolute/path/to/gin-bear` with
`bear new`; release binaries receive their matching version through build
metadata and reject requests for another framework version.

## v0.9.2 Additive Behavior

- `Authorizer` and `PermissionFairing` add resource/action/scope decisions
  without changing or replacing the existing Casbin APIs.
- Current scaffolds use strict runtime and envelope defaults. Existing projects
  keep their current configuration until they opt in.
- Current scaffolds maintain generated module registration through
  `.bear/scaffold.json`. Existing projects without that file continue to receive
  manual registration instructions.
- CORS and authentication remain opt-in; generated projects do not assume that
  either concern belongs inside the application process.

## v0.9.2 Security Behavior Changes

The following forced security changes intentionally tighten runtime behavior
in both compatibility and strict modes without removing the v0 public API:

- `LoadConfig(paths ...string) (*SysConfig, error)` is the new error-returning
  loader. `InitConfig()` keeps its old signature but now panics when a present
  configuration file is invalid; earlier versions logged parse failures and
  continued with partial/default configuration.
- YAML and JSON fields are strict by default. Development can set
  `config.strict: false` for legacy extension keys; production cannot disable
  strict loading. Explicit paths are merged in argument order before
  environment overrides.
- `server.max_header_bytes` and `server.max_request_body_bytes` default to 1
  MiB. Oversized request bodies return HTTP 413.
- An empty `server.trusted_proxies` now means no trusted proxy. Applications
  that consume `X-Forwarded-For` must list the direct proxy CIDRs explicitly.
- Client request IDs that exceed 128 characters or contain characters outside
  letters, digits, `.`, `_`, and `-` are replaced with generated IDs.
- Responses now include `nosniff`, `X-Frame-Options: DENY`, and
  `Referrer-Policy: no-referrer`. HSTS is intentionally not added by the
  framework.
- Credentialed CORS configuration cannot contain a wildcard origin; startup
  rejects that combination.
- JWT validation remains HS256-only and can require `auth.jwt_issuer` and
  `auth.jwt_audience`. `auth.jwt_clock_skew` is optional and capped at five
  minutes. JWT input is capped at 16 KiB before parsing.
- JWT validation no longer requires Redis to exist. Revocation without Redis
  returns `ErrTokenRevocationUnavailable` instead of dereferencing a nil
  client. Request authentication propagates its context to Redis checks.
- Casbin authorization uses only the `CasbinEnforcer` injected from the current
  Bear container. There is no process-global fallback, and internal enforcement
  errors return a generic client 500.
- Production WebSocket configuration rejects wildcard origins and all
  out-of-range timeout, message, and connection limits. Strict WebSocket routes
  require an explicit origin allowlist, and strict or production runtimes
  default `websocket.max_connections` to 1024.
- Fairings stop after Abort or a committed response, URI bindings remain
  authoritative over query/form/JSON values, and response handling cannot
  append a second JSON value after commitment.
- `/metrics` was removed from the default `auth.public_paths`. Add it back
  explicitly only when another access-control boundary protects it.
- The production example now requires PostgreSQL `sslmode: verify-full`, leaves
  the password empty for `POSTGRES_PASSWORD`, uses a deliberately rejected JWT
  placeholder, and shows strict loading plus explicit HTTP/JWT limits.

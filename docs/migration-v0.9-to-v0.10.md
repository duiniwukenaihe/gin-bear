# Migrating v0.9 Applications To v0.9.2

The established filename is retained for existing links, but this revision
documents the unpublished `v0.9.2` candidate. It has not been pushed, tagged,
or published. v0.9.2 strengthens production defaults, adds opt-in runtime
contracts, and changes several operational boundaries. Test the configuration
and deployment in staging before switching traffic. The examples in
`examples/migration`, `examples/basic`, and `examples/auth` are compiled by
`go test ./...`.

## Strict Migration

The compatibility defaults are `framework.strict: false` and
`framework.response_mode: raw`. Existing applications keep their historical
IoC, Fairing, lifecycle, and bare-response behavior until each runtime contract
is enabled. The `framework.strict` and `framework.response_mode` keys are
independent. Use this strict migration sequence in staging:

1. Keep configuration loading strict with `config.strict: true`. Production
   forces this setting and rejects `false`.
2. Replace panic-based startup with `LoadConfig` plus `IgniteE`, and propagate
   startup errors to the process boundary.
3. Set `framework.strict: true`, then resolve every missing, duplicate, or
   ambiguous dependency reported by `ApplyAll` or `Serve`.
4. Keep `framework.response_mode: raw` while validating handlers. Change it to
   `envelope` only after clients accept the response envelope.
5. Move process signal ownership to the application entry point and call
   `Serve(ctx)`. `Launch` remains the signal-aware compatibility wrapper.
6. Register each `CasbinEnforcer` in its current Bear container before
   attaching `CasbinFairing`; there is no process-global fallback.
7. Validate the 16 KiB JWT cap and every documented WebSocket boundary,
   including `websocket.max_connections`, before production rollout.

```yaml
config:
  strict: true
  framework.strict: true
  framework.response_mode: envelope
  websocket.max_message_bytes: 1048576
  websocket.read_timeout: 60s
  websocket.write_timeout: 10s
  websocket.ping_interval: 30s
  websocket.max_connections: 1024
```

The forced security changes apply in both compatibility and strict modes:
Fairings stop after Abort or a committed response, URI values remain
authoritative, response writers do not append a second JSON value, production
rejects wildcard WebSocket origins and all-network trusted proxies, JWT input
is capped at 16 KiB, request context reaches Redis revocation checks, and
Casbin authorization is isolated to the current Bear container.

## Compatibility Changes

### Strict Production Configuration

v0.9.2 uses **strict production configuration**. `LoadConfig(paths...)` returns
file-read, syntax, unknown-field, and validation errors; YAML unknown fields
and JSON unknown fields are rejected. Production also rejects
`config.strict: false`. `InitConfig()` keeps its v0.9 signature but panics
when loading fails, so applications that need controlled startup failure should
call `LoadConfig` and handle its returned error.

### Trusted Proxies

The default for **trusted proxies** is now an empty list. When
`server.trusted_proxies` is omitted or empty, Gin trusts no proxy and ignores
forwarded client-IP headers. Set only the CIDRs of proxies that connect
directly to the service; do not restore a broad trust-all setting.

### Request Body Limit

The default **request body limit** is 1 MiB, and the default header limit is
also 1 MiB. Requests exceeding `server.max_request_body_bytes` are rejected.
Increase the specific value only for workloads that require larger requests.

### MySQL TLS

For **MySQL TLS**, v0.9.2 reads `database.tls` as the driver TLS mode or a
registered TLS configuration name. `database.sslmode` remains a deprecated
PostgreSQL compatibility setting and is ignored for MySQL, with a warning.
Move MySQL settings to `database.tls` before deployment.

### Metrics

Newly generated `application-prod.yaml.example` files set `metrics.enabled: false`,
so production metrics stay off until that generated configuration is changed
explicitly. `NewSysConfig()` still enables metrics in its in-memory default configuration;
that compatibility default is not a global production default.

Call `EnableMetrics()` when the application should expose Prometheus metrics.
On its first call, `EnableMetrics()` skips registration only when the supplied
configuration explicitly sets `metrics.enabled: false`; otherwise it registers
the configured path (or `/metrics`). Later calls are idempotent. For existing
applications, `EnableHealth()` calls
`EnableMetrics()` when metrics configuration is absent or enabled, while an
explicit `metrics.enabled: false` still prevents registration. `/metrics` is no
longer public in generated production configuration; expose it with a protected
listener, network policy, or an explicit `auth.public_paths` entry.

### Token Revocation

Redis-backed **token revocation** is optional. JWT validation continues when no
Redis client exists, but `RevokeToken` and `IsTokenBlacklisted` return
`bear.ErrTokenRevocationUnavailable`. Callers must use
`errors.Is(err, bear.ErrTokenRevocationUnavailable)` and treat it as a failed
logout/revocation operation rather than assuming the token was revoked.

JWT parsing rejects inputs larger than 16 KiB before signature or claims
processing. Request authentication uses `ParseTokenContext` so cancellation and
deadlines reach Redis blacklist operations; the legacy `ParseToken` remains a
`context.Background()` compatibility wrapper for non-request callers.

### Casbin Container Injection

`CasbinFairing` now requires a `CasbinEnforcer` injected from the current Bear
container. Register the enforcer before `ApplyAll` or `Serve`; do not rely on
`GetByType` or a different Bear instance. Missing injection fails strict
startup. Compatibility mode fails the request with a generic 500, and internal
Casbin enforcement errors are logged without exposing their cause to clients.
Policy denials remain 403.

### WebSocket Boundaries

Production rejects wildcard `websocket.allowed_origins`; strict applications
that register a WebSocket route require an explicit allowlist before lifecycle
initialization. An omitted or zero handshake timeout uses the 10-second runtime
default; explicit values accept 100-30000 ms. The other fixed limits are 1
byte-16 MiB messages, 1s-5m reads, 100ms-1m writes, and 1s-5m pings strictly
shorter than the read timeout. `websocket.max_connections` accepts 1-100000 and
defaults to 1024 in strict or production mode. Compatibility development mode
remains unlimited only when the key is omitted. A full runtime returns 503
without upgrading.

### Dynamic Plugins

In v0.9.2, dynamic Go plugins remain experimental and are loaded only before
`ApplyAll`. A running application returns
`bear.ErrPluginHotReloadUnsupported` from `LoadPlugin` and `ReloadPlugin`.
Deploy a changed plugin by starting a new instance, passing readiness checks,
and rolling traffic over; do not attempt an in-process plugin replacement.

### Comparable Configuration Collection Fields

`AuthConfig.PublicPaths` and `WebSocketConfig.AllowedOrigins` are new in v0.9.2;
v0.9.1 did not expose these fields. They use `*[]string` so the containing
configuration structs preserve v0.9 struct comparability: a struct containing
a slice cannot be compared with `==`, while a struct containing a pointer to a
slice can.

New code should prefer the copy-safe accessors:

```go
config.Auth.SetPublicPaths([]string{"/health", "/login"})
paths := config.Auth.GetPublicPaths()

config.WS.SetAllowedOrigins([]string{"https://app.example.com"})
origins := config.WS.GetAllowedOrigins()
```

`SetPublicPaths` and `GetPublicPaths`, and `SetAllowedOrigins` and
`GetAllowedOrigins`, make defensive copies. Code that used an early unpublished
candidate with direct slice assignments must migrate to the accessors or take
the address of a local slice, in which case the caller owns aliasing:

```go
paths := []string{"/health", "/login"}
config.Auth.PublicPaths = &paths
```

The YAML and JSON keys are unchanged. An omitted or `null` value remains nil;
an explicit `[]` remains a non-nil empty list; `NewSysConfig()` keeps its
default public paths and nil allowed origins. Because `==` now compares these
fields by pointer identity, two separately allocated pointers with equal slice
contents do not compare equal. This compatibility change intentionally does
not provide value-equality semantics.

## Upgrade Procedure

1. Start from `application-prod.yaml.example` and set `BEAR_ENV=prod`.
2. Replace MySQL `database.sslmode` with `database.tls`; preserve
   `database.postgres_sslmode` for PostgreSQL.
3. Set narrow `server.trusted_proxies`, explicit request limits, and a secure
   JWT secret.
4. Decide whether metrics should be enabled and protect the metrics endpoint.
5. Update logout flows to detect `ErrTokenRevocationUnavailable`.
6. Only if the application used an early unpublished v0.9.2 candidate, migrate
   direct collection assignments to the new setter/getter APIs.
7. Follow the strict migration above, including current-container Casbin
   injection and explicit response-mode client testing.
8. Run `GOSUMDB=sum.golang.org GOTOOLCHAIN=go1.25.12 make verify` and deploy
   through a readiness-checked rollout.

## Rollback

1. Stop routing traffic to v0.9.2 and restore the previous v0.9.1 archive.
2. Restore the v0.9 configuration expected by the previous binary: use
   `database.sslmode` for MySQL if that deployment relied on it, remove
   v0.9.2-only strict runtime keys, and restore the prior request limit
   only after evaluating its security impact.
3. Keep the existing database schema; framework rollback does not require an
   automatic schema rollback. Use reviewed migration `Down` SQL only when a
   separately deployed schema migration must be reverted.
4. Verify `/ready` and `/version` on the restored binary before routing traffic
   back. Record the failed v0.9.2 commit, configuration diff, and token
   revocation availability for follow-up.

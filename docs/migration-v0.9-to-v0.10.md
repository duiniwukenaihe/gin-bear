# Migrating From v0.9 To v0.10

v0.10 strengthens production defaults and changes several operational
contracts. Test the configuration and deployment in staging before switching
traffic. The examples in `examples/migration`, `examples/basic`, and
`examples/auth` are compiled by `go test ./...`.

## Compatibility Changes

### Strict Production Configuration

v0.10 uses **strict production configuration**. `LoadConfig(paths...)` returns
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

For **MySQL TLS**, v0.10 reads `database.tls` as the driver TLS mode or a
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

### Dynamic Plugins

In v0.10, dynamic Go plugins remain experimental and are loaded only before
`ApplyAll`. A running application returns
`bear.ErrPluginHotReloadUnsupported` from `LoadPlugin` and `ReloadPlugin`.
Deploy a changed plugin by starting a new instance, passing readiness checks,
and rolling traffic over; do not attempt an in-process plugin replacement.

### Comparable Configuration Collection Fields

During the v0.10 prerelease, `AuthConfig.PublicPaths` and
`WebSocketConfig.AllowedOrigins` changed from `[]string` to `*[]string`. This
preserves v0.9 struct comparability: a struct containing a slice cannot be
compared with `==`, while a struct containing a pointer to a slice can.

Source assignments must be migrated. Prefer the copy-safe accessors:

```go
config.Auth.SetPublicPaths([]string{"/health", "/login"})
paths := config.Auth.GetPublicPaths()

config.WS.SetAllowedOrigins([]string{"https://app.example.com"})
origins := config.WS.GetAllowedOrigins()
```

`SetPublicPaths` and `GetPublicPaths`, and `SetAllowedOrigins` and
`GetAllowedOrigins`, make defensive copies. Existing direct assignments can
instead take the address of a local slice, but then the caller owns aliasing:

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
6. Migrate direct collection assignments to the new setter/getter APIs.
7. Run `GOSUMDB=sum.golang.org GOTOOLCHAIN=go1.25.12 make verify` and deploy
   through a readiness-checked rollout.

## Rollback

1. Stop routing traffic to v0.10 and restore the previous v0.9.1 archive.
2. Restore the v0.9 configuration expected by the previous binary: use
   `database.sslmode` for MySQL if that deployment relied on it, remove
   v0.10-only strict configuration keys, and restore the prior request limit
   only after evaluating its security impact.
3. Keep the existing database schema; framework rollback does not require an
   automatic schema rollback. Use reviewed migration `Down` SQL only when a
   separately deployed schema migration must be reverted.
4. Verify `/ready` and `/version` on the restored binary before routing traffic
   back. Record the failed v0.10 commit, configuration diff, and token
   revocation availability for follow-up.

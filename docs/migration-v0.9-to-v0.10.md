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

## Upgrade Procedure

1. Start from `application-prod.yaml.example` and set `BEAR_ENV=prod`.
2. Replace MySQL `database.sslmode` with `database.tls`; preserve
   `database.postgres_sslmode` for PostgreSQL.
3. Set narrow `server.trusted_proxies`, explicit request limits, and a secure
   JWT secret.
4. Decide whether metrics should be enabled and protect the metrics endpoint.
5. Update logout flows to detect `ErrTokenRevocationUnavailable`.
6. Run `GOSUMDB=sum.golang.org GOTOOLCHAIN=go1.25.12 make verify` and deploy
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

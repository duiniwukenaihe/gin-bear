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
flag. gRPC remains a compatibility-only API; its current launch path is not a
no-op, so enabling it does not produce a no-op warning.

## v0.10 Security Behavior Changes

The following changes intentionally tighten runtime behavior without removing
the v0 public API:

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
  minutes.
- JWT validation no longer requires Redis to exist. Revocation without Redis
  returns `ErrTokenRevocationUnavailable` instead of dereferencing a nil
  client.
- `/metrics` was removed from the default `auth.public_paths`. Add it back
  explicitly only when another access-control boundary protects it.
- The production example now requires PostgreSQL `sslmode: verify-full`, leaves
  the password empty for `POSTGRES_PASSWORD`, uses a deliberately rejected JWT
  placeholder, and shows strict loading plus explicit HTTP/JWT limits.

# Upgrade from v0.9.3 to v0.9.4

This guide covers the historical `v0.9.4` framework release. For the current
release, continue with the [v0.9.5 upgrade guide](upgrade-v0.9.4-to-v0.9.5.md). Test the upgrade in an
application checkout and keep its current configuration, migrations, and
database backup available for rollback.

## Before changing the dependency

- Use Go 1.26.6 for the framework, generator, and optional nested modules.
- Record the application's existing `go.mod`, build tags, configuration,
  migration history, and deployed binary. `v0.9.3` and its generator remain
  available as the rollback target.
- Read [compatibility](compatibility.md) and the full
  [changelog](../CHANGELOG.md). The new core APIs are additive, but stricter
  startup, authentication, route authorization, and generated project
  behavior can reveal previously tolerated configuration errors.

## Runtime and configuration

Existing applications keep compatibility defaults unless they opt in to
`framework.strict: true` and `framework.response_mode: envelope`. Production
configuration rejects unknown keys; production compatibility runtime needs
the explicit migration escape hatch
`framework.allow_compatibility_in_production: true`. Prefer removing that
escape hatch after making startup and registration errors explicit with
`IgniteE`, error-returning registration, and `Serve`.

Check `BEAR_ENV=prod`, `GIN_MODE=release`, JWT secret provisioning, trusted
proxy CIDRs, request limits, and protected diagnostics. New scaffolds do not
put `/metrics` or `/version` in public authentication paths. Redis-backed
token revocation needs a reachable Redis store; an unavailable required store
must fail startup or the revocation operation, never silently succeed. See
[production configuration](production.md#configuration) for the supported
environment overrides. Verify the application's existing routes still return
the intended 200, 400, 401, and 403 responses before traffic is moved.

PostgreSQL production transport is an explicit choice: `verify-full` for
hostname-verified TLS or `disable` for a trusted plaintext link. MySQL uses
`database.tls`; the old `database.sslmode` key is ignored for MySQL with a
warning. `database.enabled: false` prevents database startup; selecting
`database.type: postgres` opens PostgreSQL without starting SQLite. Enable
only the stores and plugins the service actually uses.

## Casbin and optional dependencies

The default build retains the legacy Casbin/GORM behavior. The new
`CasbinAuthorizer` makes policy revocation effective for subsequent decisions
and fails closed on persistence/reload errors. Persistent authorization reads
policy before each decision, including a database read per request; measure
that cost under the application's authorization load before enabling it on a
high-traffic route.

For a PostgreSQL-backed Casbin service, review existing `casbin_rule` rows,
copy and version
[`001_create_casbin_rule.up.sql`](../migrations/optional/casbin-postgres/001_create_casbin_rule.up.sql)
in the application's own migration history, and apply it **before** deploying
the new service. Do not drop policy tables as an automatic rollback. Wire
`NewPostgresCasbinAdapter` to the application's PostgreSQL pool and register
`NewCasbinAuthorizerWithAdapter` plus the appropriate authorization fairing;
creating the bean alone does not enforce routes.

The service can then build with
`-tags bear_no_mysql,bear_no_sqlite,bear_casbin_no_gorm_adapter`. Without
Casbin, use `bear_no_casbin` instead of the adapter tag. The default build
needs no opt-out tags. Tags remove compiled drivers and adapters, while the
root `go.mod` still includes their modules; they do not yet shrink module
downloads. A tag excluding the configured database causes an explicit
startup failure, so test each deployment configuration against its build.

## Generated projects and Agent extension

After the final root tag is published, install the matching `bear` generator
and use it to create **new** projects. Do not regenerate an existing project
over local code. For existing projects, preview generation or migration,
review the file diff and SQL, then apply the reviewed change. Generated SQL
migrations are separate deployment steps, not startup side effects. Use
`bear doctor --format json --probe` to inspect the resulting project; probes
are read-only.

The Agent runtime and MCP bridge are separate experimental modules. The root
tag does not make their `go get` versions available: wait for the respective
directory-prefixed tags and test the external consumer before using them in
a production application. Do not enable an Agent route merely because the
module exists; provide trusted caller identity, tenant filtering, per-call
authorization, audit storage, and provider credentials deliberately.

## Acceptance and rollback

For each application, run its tests and build with the intended tags, apply
migrations to a disposable copy of the production schema, and exercise login,
authorization and revocation, CRUD, readiness, shutdown, and any Agent tools
that are enabled. Test with the same database engine and transport mode used
in production. The framework's own `make verify`, real PostgreSQL/MySQL/Redis
integration suite, and clean `make verify-rc` result are prerequisites to the
final tag; application acceptance remains a separate gate.

If the application fails, restore its previous `go.mod` pin and binary, then
redeploy `v0.9.3`. Keep migration rollback separate from binary rollback:
review data written under the new schema before reversing SQL. Do not move a
published tag to represent a fix; release a new version instead.

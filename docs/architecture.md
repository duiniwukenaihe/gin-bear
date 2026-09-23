# Architecture

## Request path

`Bear` owns a Gin engine plus global Fairings. A request flows through
authentication (JWT, optional Redis revocation) and then ordered Fairings:
`CasbinFairing` (role checks against the container-injected `CasbinEnforcer`)
or `PermissionFairing` (resource/action checks against an `Authorizer`).
Controllers are thin typed handlers (`BuildE`/`HandleE`); business logic lives
in Services, persistence in Repositories embedding `bear.Repository[T]`.

`Repository.DB` preserves the `bear_db_tx` transaction while normalizing a
`*gin.Context` to its HTTP request context, so cancellation, deadlines, and
request-scoped values reach GORM. Plain contexts pass through unchanged.

## Authorization: two doors

- `CasbinEnforcer` (legacy, compatibility-preserved): embeds
  `*casbin.CachedEnforcer` with the decision cache disabled by default, so
  sequential revocation is immediate. Not safe for concurrent authorization
  plus policy writes.
- `CasbinAuthorizer` (controlled): private enforcer plus one `RWMutex`,
  fail-closed on persistence/reload errors, three-parameter RBAC only, rejects
  non-empty `Scope`. Use it for online policy changes with route-level
  `PermissionFairing`. Each instance owns independent in-memory policy; a
  shared database is not shared memory until every instance `LoadPolicy`s.

## Configuration

`bear.LoadConfig` merges `application.yaml`, the `BEAR_ENV`/`GIN_MODE`
overlay (with compatibility filename rules), `config.json`, then environment
overrides, followed by `PostProcess`, `Validate`, and production security
checks. Generation uses `LoadDatabaseConfigForGeneration`: same discovery and
decoding, no runtime secrets, no connections.

## Generation pipeline

`bear new` renders `internal/scaffold/template` (`--profile production` adds
the profile subtree) and writes `.bear/scaffold.json` (manifest v1).
`bear gen api` resolves the database
contract once, renders resource templates into a temp dir, publishes
atomically, writes the migration pair, pins dependencies, and registers the
module — holding `.bear/generate.lock`, rolling back on failure, never
rewriting applied migrations. Every `api` generation takes that lock, managed
or legacy (a manifest-less project still writes migrations), and a rollback
removes the resource package, any partially written migration pair, and
restores `go.mod` pins.

`bear gen api --dry-run --format json` renders the identical plan without
writing anything, and `bear gen apply --plan` re-verifies inputs, project root,
and content digests before executing it (stale plans are rejected). A previewed
apply upgrades the manifest to v2 with per-file digests.

Migrations are reviewed SQL applied by the standalone `cmd/migrate` step via
`MigrationRunner` (history + locking tables); the server never migrates at
startup.

## Observability

Health (`/live`, `/ready`), metrics, and tracing are framework-owned.
Logs/metrics labels never carry secrets, tokens, DSNs, or prompt text;
`PermissionFairing` and Casbin paths log internal detail server-side while
returning generic 403/500 publicly.

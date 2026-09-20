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
rewriting applied migrations. A `--dry-run` preview reusing the same render
and manifest content digests are planned follow-ups, not current behavior.

Migrations are reviewed SQL applied by the standalone `cmd/migrate` step via
`MigrationRunner` (history + locking tables); the server never migrates at
startup.

## Observability

Health (`/live`, `/ready`), metrics, and tracing are framework-owned.
Logs/metrics labels never carry secrets, tokens, DSNs, or prompt text;
`PermissionFairing` and Casbin paths log internal detail server-side while
returning generic 403/500 publicly.

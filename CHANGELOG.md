# Changelog

All notable changes to gin-bear are documented in this file.

## [Unreleased]

### Added

- Controlled `CasbinAuthorizer` (`NewCasbinAuthorizer`) for online policy
  changes behind the existing `Authorizer`/`PermissionFairing` contract: one
  `RWMutex` for reads/writes/reloads, immediate revocation for authorizations
  started after a successful write, fail-closed reads after persistence/reload
  failures, three-parameter RBAC only with explicit `Scope` rejection, and
  independent in-memory policy per instance; persistent instances reload
  policy before each authorization so cross-instance revocation is visible
  on the next decision, with a database read per authorization.
- Versioned engineering docs (`docs/development.md`, `docs/architecture.md`,
  `docs/recipes/`) and `bear agent init`, which scaffolds the local,
  never-committed `AGENTS.md` entry, maintains `.gitignore`, and verifies
  ignore/untracked state without staging anything.
- `bear doctor [--format json] [--probe]`: static read-only diagnosis
  (project, config, secrets, manifest, framework, database) with a versioned
  JSON contract, 0/1/2 exit codes, redacted credentials, and bounded probes.
- `extensions/agent` module (own go.mod, experimental): single read-only
  runtime agent on eino v0.9.19 (fake plus explicit OpenAI-compatible vendor,
  SSE/JSON endpoints, budgets, per-call authorization, tenant-scoped example),
  durable tasks with approval-gated writes (leases, fencing, idempotency,
  recovery), deterministic evals with a zero-bypass safety gate, audit
  retention, bounded metrics, and alert thresholds.
- Optional `tools/bear-mcp` module (own go.mod): stdio MCP bridge with three
  read-only tools (project_info, doctor, gen_preview) over a pinned CLI path,
  bound project roots, and output/time limits; no writes, no credentials.
- Version support matrix and release checklist (`docs/supported-versions.md`).
- Deterministic generation preview (`gen api --dry-run --format json`) sharing
  the exact render with real generation, plus `gen apply --plan` with
  re-verified inputs, stale-plan rejection, path/symlink containment, and
  manifest v2 digest upgrades. Generated service samples now cover pagination
  bounds, CRUD round trips, not-found mapping, and cancellation.
- `bear new --profile production`: deployment assets (README, Makefile,
  multi-stage non-root Dockerfile, .dockerignore, project CI, dev-only
  compose) while the default stays minimal; embed directive now covers
  dot-paths at any depth and the default render skips the profile subtree.
- `tests/integration/` plus `scripts/test-integration.sh`, a pinned
  compose file, and CI/release gating: real PostgreSQL/Redis acceptance
  (generated-app migration and CRUD, rollback, cancellation, interruption
  recovery, revocation reload, Redis round trip and designed failures) with
  disposable credentials and redacted logs; MySQL runs when a DSN is provided.
- Generation-only `LoadDatabaseConfigForGeneration` plus repeatable
  `bear gen api --config <path>`: generation follows the runtime file chain
  (base, `BEAR_ENV`/`GIN_MODE` overlay, `config.json`, environment overrides)
  without connecting to a database or requiring production secrets.

- Optional gRPC production runtime contracts for injectable
  `GRPCServiceRegistrar` services, unary/stream interceptors, TLS and mTLS,
  loopback-only proxy plaintext, health, reflection opt-in, resource limits,
  recovery, logging, and coordinated shutdown.
- Context-bounded database and Redis startup APIs, including lifecycle-owned
  Redis initialization for required authentication revocation storage.
- Redis TLS 1.2+, custom CA, and optional mutual-TLS client certificate support.
- Per-runtime HTTP totals through `Runtime.Requests` and `Runtime.Errors`.
- `bear gen api` writes the reviewed SQL migrations for the table it generates,
  using the `migrations/001_create_<table>.up.sql` and `.down.sql` layout
  documented in `docs/production.md`. The dialect follows `database.type`, and an
  existing file is never overwritten, so a migration that has already been
  applied keeps its version. New scaffolds ship a `cmd/migrate` one-off tool that
  applies or rolls those files back, keeping schema changes a separate deploy
  step rather than something startup does implicitly.

### Changed

- PostgreSQL production connections now accept either explicit plaintext
  (`sslmode=disable`) or hostname-verified TLS (`verify-full`); downgrade and
  unverified TLS modes remain rejected. Generated production configuration
  shows the operator's choice.
- Optional `bear_no_casbin` and `bear_no_sqlite` build tags let applications
  that use neither feature omit both from the compiled binary while the
  default build remains compatible. Casbin's GORM adapter itself imports
  SQLite, so both tags are needed to remove SQLite completely.
- PostgreSQL-only services can exclude MySQL with `bear_no_mysql`. Casbin can
  use the application's PostgreSQL pool through `NewPostgresCasbinAdapter`
  and `NewCasbinAuthorizerWithAdapter`; `bear_casbin_no_gorm_adapter` excludes
  the legacy adapter's SQLite and SQL Server imports. Policy schema creation
  is an explicit optional migration.
- Durable write tasks retain their unknown-result reconciliation state when
  canceled or timed out after execution intent; a version check prevents a
  cancellation on another instance from erasing a newly recorded intent.
- The factory-built `CasbinEnforcer` disables the decision cache by default so
  role/policy removal takes effect on the next enforcement. The exported type,
  constructor signature, and promoted methods are unchanged.
- `CasbinAuthorizer` validates policy/grouping arity against the model before
  writing, so malformed rules are rejected without touching memory or the
  database. Memory-mode `LoadPolicy` returns `ErrCasbinReloadRequiresAdapter`
  instead of panicking and keeps the existing policy usable.
- `Repository.DB` preserves the `bear_db_tx` transaction while normalizing a
  `*gin.Context` to its request context, so cancellation, deadlines, and
  request-scoped values reach GORM. Operations that previously ignored
  cancellation now return `context.Canceled`/`DeadlineExceeded`.

- The pinned Go toolchain moved from `go1.25.12` through `go1.25.14` to
  `go1.26.6` (all three modules' `go` directive and the pinned `GOTOOLCHAIN`),
  and the
  dependencies carrying reachable vulnerabilities were raised, so `govulncheck`
  reports no reachable vulnerabilities: `google.golang.org/grpc` v1.82.1 → v1.83.2
  (GO-2026-6348, GO-2026-6443), `golang.org/x/net` v0.53.0 → v0.58.0
  (GO-2026-5026), plus the standard-library fixes shipped by the patch release
  (GO-2026-6088, GO-2026-6089, GO-2026-6090, GO-2026-6091, GO-2026-6218,
  GO-2026-5972). `x/crypto` v0.56.0 still carries GO-2026-5932, which has no
  fixed release and is not reachable on any call path; it is recorded as
  accepted risk. `x/mod`, `x/sync`, `x/sys`, `x/text`, the
  OpenTelemetry modules, and the `genproto` pseudo-versions move forward as their
  requirements. The OpenTelemetry upgrade deprecated `attribute.Value.Emit`, so
  the one caller — the tracing redaction test — now uses
  `attribute.Value.String`, which returns the same `stringly` value for a STRING
  attribute; the leak assertion was re-verified by temporarily reintroducing an
  `error.message` attribute and watching the test fail. New scaffolds write
  `go 1.26.6`, and the documented `make verify` command uses
  `GOTOOLCHAIN=go1.26.6`. The runbook's dated v0.9.2 audit sections keep the
  toolchain they actually recorded.
- `cmd/bear` no longer carries a second copy of the resource-name-to-identifier
  helpers. Unifying the commands moved generation into `internal/cli`, which owns
  `nameParts`/`titleName`, and the copy in `cmd/bear` was left behind: `main`
  never called it, its own test was its only consumer, and it had already
  diverged from the live implementation (no lower-casing, fewer separators, and
  no empty or leading-digit fallback). The linker had already dropped both
  functions, so `__TEXT` (40206336 bytes) and `__text` (20575664 bytes) are
  identical in both builds; only the `__LINKEDIT` and `__DWARF` debug metadata
  differs.
- The package-level `TotalRequests` and `TotalErrors` counters aggregate every
  runtime in the process, which is misleading when one process hosts more than
  one Bear, and reading them requires `atomic.LoadInt64`. They remain available
  and are still updated for compatibility, but are now deprecated in favour of
  `Runtime.Requests` and `Runtime.Errors`.

- New scaffolds make `auth.enabled: false` explicit, prohibit compatibility
  runtime in production by default, keep gRPC disabled, and show only commented
  production transport guidance without generated certificates.
- Runnable examples and the primary README path use `IgniteE`, error-returning
  registration APIs, and `Serve`; legacy APIs remain available.
- `OpenAPIConfig.Apps`, `TimeWindow`, `ReplayCheck`, and `HeaderPrefix` remain
  compatibility fields but are deprecated and do not provide request signing.
- Automatic authentication uses an effective explicit `AuthFairing`, validates
  plugin route policies, runs browser preflight safely, and enforces declared
  Redis revocation storage in production. The legacy `file` storage value is a
  warned alias for stateless `jwt` validation.
- Manual and automatic authentication share one outer middleware, validate the
  effective JWT policy, and fail closed when Redis revocation is declared but
  unavailable. Plugin routes use full grouped paths and pass through caller
  middleware before fallback dispatch; ambiguous dynamic route shapes fail
  registration instead of selecting a handler by registration order.
- Global non-authentication Fairings guard native `GET`/`POST` and related Gin
  routes, including routes added through `Bear.Group`/`GroupE`, as well as
  compiled handlers and the framework metrics endpoint, closing
  authorization-policy bypasses. Plugin fallback dispatch no longer depends on
  Gin `NoRoute`, so existing custom 404 handlers cannot remove plugin routes.
- Framework defaults and generated applications no longer place `/metrics` or
  `/version` in authentication public paths; operators must expose diagnostics
  explicitly behind an intended access-control or network boundary.
- Native Gin, grouped, metrics, opaque-handler, and WebSocket paths now unwind
  entered Fairings exactly once after request handling without rewriting
  response bytes owned by Gin handlers.
- Tag-triggered GitHub releases now run the complete pinned `make verify`
  quality gate against the tagged commit before publishing the release.
- HTTP and gRPC shutdown track active handlers, reject handlers that arrive
  after draining begins, and retain lifecycle resources when a non-cooperative
  handler outlives the total shutdown budget.
- Shutdown irreversibly seals the Bear instance before lifecycle resources
  close, compatibility Fairings cannot replace authentication after startup,
  and Redis production/tracing decisions are scoped to the owning Runtime.
  Redis tracing binds the owning TracerProvider, standalone remote Redis
  connections require TLS, and compatibility Build-time Fairing mutation fails
  startup after lifecycle registration closes.
- Development generators require an explicit local replacement and cannot mix
  unreleased HEAD templates with a published framework tag.
- The scaffold's database switch is documented instead of implied. A fresh
  project still ships `database.enabled: false`: `pkg/bear/bear.go` treats
  `GIN_MODE=release` as production, production rejects SQLite as an unsupported
  database type, and enabling SQLite by default would therefore stop a fresh
  `bear new` project from starting under release mode, which the baseline could
  do. The template carries the SQLite block to uncomment and says why a real
  deployment needs MySQL or PostgreSQL; `application-prod.yaml.example` keeps
  PostgreSQL and adds the migration deploy-step reminder. Generated projects also
  receive a `.gitignore` that excludes the local SQLite files, the `cmd/server`
  and `cmd/migrate` build output, and the coverage profile, while keeping
  `migrations/` committed.
- The readiness, HTTP-shutdown, and gRPC-shutdown tests no longer prove
  concurrency or fail-fast behaviour with tight wall-clock thresholds. Two 100ms
  readiness checks finish in ~103ms but were asserted to finish inside 180ms,
  and a deferred `Shutdown` that refuses to re-wait costs one 25ms forced-sync
  window against a 100ms bound, so each assertion had under 80ms of slack and
  failed whenever the scheduler stalled. Concurrency is now proven by the
  overlap counter and the refusal by its forced-shutdown marker, and the
  remaining clock bounds are hang guards sized to the configured budget.
- `docs/compatibility.md` claimed that every compatibility-only API carries a Go
  `Deprecated:` comment, which its own list contradicted: `pkg/bear/gen` is
  compatibility-only and carries none. It is the only exception, and the marker
  cannot be added — the v0.9.1 baseline consumer calls `gen.NewGenerator`, and the
  gate runs `staticcheck` over the whole module, so the resulting `SA1019` report
  would fail the build. The contract now records the exception, and the package
  documentation explains why the marker is deliberately absent.
- The warning list in `docs/compatibility.md` read as if its ten keys were the
  whole set. `Ignite` logs thirteen warnings from the same
  `compatibilityWarnings()` call: the deprecated `auth.storage_type: file`
  alias, an explicitly enabled production compatibility runtime, and a
  `database.sslmode` that MySQL ignores also warn from that path. Each of the
  three has its own test, and the contract now states that the list is not
  exhaustive.
- The scaffold critical coverage group sat exactly on its 80% threshold. The
  precise figure was 132/165, so `100 * covered` equalled `80 * total`: any single
  covered statement moving out of coverage, or any single new uncovered statement,
  would have failed the release gate. `internal/scaffold` now tests the
  `--framework-replace` validation that guards `bear new` (the version and
  replacement pairing, the absolute-path and control-character checks, and the
  directory, `go.mod`, and framework-module checks) and the manifest failure
  stages that distinguish a missing manifest from a corrupt one, taking the group
  to 145/165 with real headroom. Three of the group's remaining uncovered
  statements are unreachable by construction and are kept as defensive code.

### Fixed

- Approval/task state changes are transactional: submit (approval plus task
  insert), approve (consume plus queue), and unknown requeue (fresh approval
  plus move) commit or roll back together, with conditional writes still
  fencing across instances and lock-contention retries for transient writer
  conflicts. New crash-injection coverage proves rollback and single-winner
  behavior, including against real PostgreSQL.
- Workers renew task leases with a heartbeat, so executions longer than the
  lease complete instead of being reaped mid-flight; renewal never touches
  versions, and fencing still guards every state change.
- Task claim/complete racing across instances: `Claim` is now an atomic
  conditional UPDATE with loser retry, and `Complete`/`Cancel` commit
  conditionally so cancellation wins over late results.
- Crashed unconfirmed writes no longer blind-retry: the execution intent and
  budget reservation persist across crashes, recovery moves them to `unknown`
  for reconciliation (`ConfirmUnknown`/`RequeueUnknown`), and requeue demands
  a fresh approval.
- Nested-module CI invoked a root-relative script from module working
  directories; it now uses the workspace-absolute script path.
- `scripts/test-integration.sh` no longer forces maintainer-machine cache
  paths on other hosts (explicit env wins, otherwise `go env` defaults), and
  refuses to run below 10 GiB free disk.
- Agent runs now gate vendor calls on remaining budget, send `max_tokens`
  caps, request stream usage, and reconcile reports against conservative
  reserves instead of accounting purely after the fact.
- Runner identity is bound into the run context, so business tools observe
  the same trusted identity over HTTP that `Authorize` sees.
- Vendor SSE streams incrementally with per-event flushes and socket write
  deadlines; dead clients terminate the handler instead of parking it.
- `gen apply` replays the preview's explicit `--config` selection instead of
  always resolving the default chain.
- `model`/`dto` generation again pins `shopspring/decimal` for decimal
  fields; only `api` plans database migrations.
- Vendor tool definitions now carry the declared JSON-Schema parameters, and
  invocations validate types, required fields, enums, and unknown fields
  before authorization.
- `docs/production.md` presented `examples/migration/main.go` as the tested
  production-loading pattern, but the package had no test file, so
  `loadProductionConfig` was only ever compiled and never executed. The example
  now carries a test that loads a production configuration, rejects SQLite and a
  relaxed `config.strict`, and proves that load failures reach the caller wrapped
  in the example's own context rather than as a panic.
- The `pkg/bear/gen` scanner treated any struct tag containing the substring
  "inject" as an injection directive, so `json:"inject_total"` or
  `gorm:"column:inject_id"` produced a `bear.Resolve` line that overwrote the
  field with a bean the runtime never asked for. It now matches the tag key
  exactly, which is how the runtime decides in `pkg/bear/ioc.go`.
- The exported `pkg/bear/gen` `ServiceTemplate` renders a service package that
  does not compile, because it imports `pkg/bear` and references nothing from it.
  The import is retained: the constant's value is part of the pinned v0.9.1
  public API baseline, and `scripts/check-api-compat.sh` rejects a value change as
  non-additive. The limitation is documented on the constant and pinned by its own
  test, so removing it later is a deliberate baseline update rather than a silent
  drift. The other exported template, `ControllerTemplate`, does render a
  compilable package, and both are now rendered and built in a fixture module.
- The `pkg/bear/gen` regression test promised a compilable injector but only ran
  `format.Source`, which parses without resolving an identifier, and it generated
  into a package other than the scanned one. That layout cannot compile: the
  rendered file names the scanned structs unqualified, and `NewGenerator` only
  receives a package name. The rendered file is now built for real in a fixture
  module, the cross-package limit is pinned by its own test, and the layout
  contract is documented on the package and in `docs/supported-features.md`.
- Compatibility-mode `Mount`, `Beans`, and `AddModule` now publish bean
  metadata and append their registration records under the shared registration
  lock. Writing the `Bear` expression metadata without synchronization raced
  with the request-time OpenAPI path that reads it through `authFairings`, and
  `GenerateOpenAPI` now reads a stable route-metadata snapshot instead of the
  live registry.
- Generated injectors registered through `RegisterRuntimeStaticInjector` now
  resolve by package-qualified key before falling back to the historical bare
  struct name, and the framework registers its own legacy injectors under both
  keys. Two same-named types from different packages could previously shadow
  each other, and a generated injector for a user type sharing a framework type
  name overwrote the framework entry, whose unchecked type assertion then
  panicked on the framework type.
- Compatibility-mode dependency injection now logs a warning when an
  `inject`-tagged field cannot be resolved instead of leaving the field at its
  zero value silently. An unexported field that must be injected panics with an
  error value rather than a string.
- Plugin module registration marked its route-registration window with a plain
  `Bear` field that was written while holding only the plugin barrier and read
  while holding the route registration lock, so a concurrent registration race
  was possible. The flag is now atomic and restores its previous value instead
  of clearing unconditionally, so back-to-back plugin registrations no longer
  clobber each other's mode.
- The generated-project watcher published its restarted `*exec.Cmd` from an
  unguarded goroutine while another restart read it under the watcher mutex,
  and `Start` waited on a channel nothing could close, so a closed watch stream
  left the caller blocked forever. Restarts now assign the command under the
  mutex, `Start` returns when the watch stream ends, `.git` and `vendor`
  subtrees are skipped instead of walked, and restarted processes run in the
  watched directory. `RunOnce` honours its directory argument, and
  `LoadConfigForCLI` reads the target directory's configuration without
  mutating the process working directory.
- The process-wide Gin mode reservation was a latch: once any strict runtime had
  reserved a mode it was never released, so a strict runtime created after the
  previous one had shut down was still rejected with `ErrGinRuntimeConflict`.
  The reservation is now owned by the reserving runtime's lifecycle and dropped
  once that lifecycle has stopped, while a conflicting mode is still rejected
  for as long as any strict runtime remains live.
- The process-wide compatibility facade was a single slot that each `Ignite`
  overwrote, so once a runtime shut down the package-level helpers (`GetByType`,
  `GetInjector`, and the logger target reached on every log record) kept
  resolving against a stopped runtime. Publishing now remembers the facade it
  replaced, and reading falls back to the newest still-live one, clearing to no
  facade once every runtime has stopped. Liveness is a lock-free flag on the
  lifecycle so the logging fast path never contends on the lifecycle mutex.
- `bear gen api` published a resource that could not start, silently. The
  generated repository always injects `*bear.GormAdapter`, but a disabled
  database registers no adapter while `EnableDatabaseE` stays silent, so strict
  startup failed with `bean missing: dependency *bear.GormAdapter` and nothing
  pointed at the configuration. Generating into a project whose `application.yaml`
  sets `database.enabled: false` now warns on stderr and prints the block to add.
  Generation is not refused: an application may register `*bear.GormAdapter`
  itself, which is what the release E2E does with an in-memory SQLite handle. The
  scaffold keeps the database disabled by default, because enabling a SQLite
  database would make `GIN_MODE=release` reject the scaffold's own default as an
  unsupported production database.
- `bear gen api` produced no schema. A generated repository queries a table that
  nothing created, so the first request failed even once the adapter resolved.
  Resource generation now writes `migrations/NNN_create_<table>.up.sql` and the
  matching `.down.sql` for the configured dialect, and the scaffold ships a
  `cmd/migrate` tool to apply them as a separate deploy step.
- A `.bear/generate.lock` left behind by an interrupted `bear gen` permanently
  blocked every later generation with a bare `file exists`. The lock now records
  the owning pid and start time, and a conflicting run reports that owner plus
  the exact command that clears the lock. A stale lock is still not reclaimed
  automatically, because deciding that no other process is generating belongs to
  the operator.
- The legacy `pkg/bear/gen` code generator could not produce working output.
  `Scanner` rendered every composite field type with `fmt`'s debug form, so a
  `*Repository` field became `&{%!s(token.Pos=90) Repository}` and the generated
  injector did not compile; field types are now rendered as Go source.
  `Generator.Generate` failed on every call because its template asked for a
  module name that was never supplied, and it now writes the framework import
  path the package's other templates already use, formats its output, and creates
  the destination directory the way `GenerateFromTemplate` already did.
- Generated Windows servers did not shut down gracefully when their console
  window was closed, the user logged off, or the machine shut down. The earlier
  fix for an unsupported `syscall.SIGBREAK` reference also dropped
  `syscall.SIGTERM`, which does exist on Windows: the runtime reports it for
  `CTRL_CLOSE_EVENT`, `CTRL_LOGOFF_EVENT`, and `CTRL_SHUTDOWN_EVENT`, and the
  framework's own `Launch` already listens for it. The generated signal set is
  now `os.Interrupt`, which covers Control-C and Control-Break, plus
  `syscall.SIGTERM`, and the regression test type-checks the rendered file for
  `GOOS=windows` instead of matching its text, so an unsupported constant now
  fails the build rather than reaching a generated project.

## [v0.9.3] - 2026-08-12

### Fixed

- Fairing recovery now treats `http.ErrAbortHandler`, broken pipes, and reset
  connections as request termination instead of appending an HTTP 500 or
  logging a misleading panic.
- Query, form, and URI binding supports Gin 1.12's explicit
  `parser=encoding.TextUnmarshaler` contract, including pointer fields and
  propagated decoding errors.
- Generated Windows projects use only signals supported by Windows, and
  release-governance tests that execute shell scripts are limited to Unix.

### Changed

- Releases follow the framework-style Go module model: an immutable semantic
  version tag, generated release notes, and GitHub-generated source archives.
  Applications consume the framework with `go get` or a `go.mod` requirement;
  platform-specific archives and GoReleaser are not part of the release
  surface.
- The optional project generator reads its module version from Go build
  information, so installing a tagged generator such as
  `go install github.com/duiniwukenaihe/gin-bear/cmd/bear@v0.9.3` generates
  projects pinned to that same version without custom linker flags.
- CI failures now retain concise command diagnostics, and releases explicitly
  trigger Go Module indexing after publication.

## [v0.9.2] - 2026-08-12

### Added

- Resource-level authorization through `Authorizer`, `PermissionFairing`, and
  request-derived subject and scope resolvers. Authorization storage remains an
  application concern and is not coupled to Casbin or a database schema.
- Error-returning strict registration APIs for modules, controllers, Fairings,
  routes, middleware, health, metrics, tracing, database, and WebSocket setup.
- A minimal `.bear/scaffold.json` project registry and generated
  `internal/app/modules_gen.go`, allowing `bear gen api` to register generated
  modules automatically.
- Generated CRUD validation, `PATCH` support, correct `201`/`204` statuses, and
  deterministic `400`/`404` behavior.

### Changed

- Invalid environment overrides and invalid port, pool, timeout, CORS, and
  tracing endpoint settings now fail configuration loading instead of falling
  back silently.
- Strict startup builds routes before lifecycle startup, seals Bear-managed
  registration, initializes each aliased component once, and rolls back
  pre-opened database and tracing resources when startup fails.
- Generated applications use strict runtime and envelope response defaults,
  explicit error-returning startup APIs, and separate metrics and health setup.
  CORS and authentication remain opt-in.
- Generated project dependencies use their real module paths and preserve a
  higher compatible version already selected by the application.

### Fixed

- Gin abort and committed-response semantics consistently stop Fairing and
  handler execution without appending another response or forcing HTTP 400.
- Strict IoC reports missing, ambiguous, and duplicate dependencies at startup,
  preserves deterministic lifecycle order, unblocks concurrent waiters after
  injector panics, and permits a failed injection attempt to be retried.
- Readiness name panics, unsafe `StatusResponse` values, OpenAPI envelope drift,
  migration version/name mismatches, partial rollback plans, audit-field
  mutation, optimistic-lock caller mutation, and zero-row update ambiguity.
- Failed `IgniteE` construction restores Gin process globals and does not
  publish a partial default runtime.

### Compatibility Boundary

- Strict registration guarantees apply to Bear-managed APIs. The embedded
  public `*gin.Engine` and returned raw `*gin.RouterGroup` remain v0 compatibility
  escape hatches; direct mutation and concurrent application registration are
  unsupported after startup begins. Removing those escape hatches requires a
  v0.10 API change.
- The formal release gate runs against the exact clean annotated tag and is
  repeated by release CI before publishing archives and checksums.

### Runtime And Operations

- Production configuration loading with strict decoding, environment overrides,
  and validation errors returned by `LoadConfig`.
- Per-runtime Prometheus metrics, bounded readiness checks, tracing, and
  generated OpenAPI validation.
- A CLI-only release process for `cmd/bear` archives, SHA-256 checksums, source
  archives, and release metadata.
- Opt-in strict runtime contracts through `framework.strict`, independent raw
  or envelope responses through `framework.response_mode`, and
  error-returning `IgniteE` and `Serve` startup APIs.

### Security And Compatibility

- Production defaults now require explicit trusted proxies, request body
  limits, and safe configuration.
- MySQL uses `database.tls`; legacy `database.sslmode` is ignored for MySQL.
- Redis-backed token revocation reports a typed availability error when Redis
  is not configured.
- Existing applications retain the compatibility defaults
  `framework.strict: false` and `framework.response_mode: raw`. Strict mode is
  an explicit migration; the security boundary fixes below apply in both
  modes.
- JWT input is capped at 16 KiB before parsing, request context reaches Redis
  revocation checks, and Casbin enforcement uses only the current Bear
  container's injected enforcer.
- Production rejects unsafe WebSocket origins and out-of-range resource
  limits. Strict and production runtimes default to 1,024 concurrent
  WebSocket connections; compatibility development remains unlimited when no
  limit is configured.
- Strict route Fairings and WebSocket handlers now fail startup on missing
  dependencies, module Beans are injected before Build, and cancellation no
  longer hides rollback failures.
- A Bear serving lifecycle is single-use, and an established strict Gin mode
  cannot be overwritten by a compatibility instance with a conflicting mode.

### Upgrade Notes

Read [the v0.9.1 to v0.9.2 migration guide](docs/migration-v0.9.1-to-v0.9.2.md)
before deploying. It separates compatibility defaults, strict opt-in behavior,
forced security changes, and rollback steps.

### Release Candidate Verification

- Local release-candidate verification raises the repository coverage gate to
  70%. A development-time profile recorded 2,822/3,723 statements (75.8%);
  complete handler and lifecycle chains are 82.9% and 84.9%, and every other
  manifest-backed critical group exceeded 80%. These figures are diagnostics,
  not current-commit release evidence.
- A committed v0.9.1 module API manifest and pinned official `apidiff` gate
  cover every public Go package, permit additions, reject incompatible changes,
  and compile a separate v0.9 consumer fixture without relying on local tags.
- A release-only end-to-end test builds both a v0.9-style fixture and a newly
  generated application through the public CLI, preserves generated `app.go`,
  exercises `gen api`, verifies health, success, validation, authorization,
  `SIGTERM` shutdown, and rejects secret values in bounded captured logs and
  traces.
- `make verify-rc` records commit/tool versions, shuffle seed, per-step logs and
  exit codes, and worktree hygiene; release CI uploads that evidence while the
  ordinary `make verify` target remains free of shuffle20/race3 repetition.
- The run associated with commit `1db2743e3b1146ecc6592e0ea46cfa4e5ad311c1`
  used a dirty worktree and is retained only as development-time validation;
  it is not evidence for the current commit. Formal release-candidate evidence
  requires a complete `make verify-rc` run from the clean, committed fixes.
- These historical development diagnostics are not formal release-gate
  evidence for `v0.9.2`.

## [0.9.1]

- Last v0.9 maintenance release.

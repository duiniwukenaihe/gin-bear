# Changelog

All notable changes to gin-bear are documented in this file.

## [v0.9.3] - Unreleased

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
- This candidate has not been pushed, tagged, or published.

## [v0.9.2] - Unreleased

### Added

- Production configuration loading with strict decoding, environment overrides,
  and validation errors returned by `LoadConfig`.
- Per-runtime Prometheus metrics, bounded readiness checks, tracing, and
  generated OpenAPI validation.
- A CLI-only release process for `cmd/bear` archives, SHA-256 checksums, source
  archives, and release metadata.
- Opt-in strict runtime contracts through `framework.strict`, independent raw
  or envelope responses through `framework.response_mode`, and
  error-returning `IgniteE` and `Serve` startup APIs.

### Changed

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

Read [the v0.9.2 strict migration guide](docs/migration-v0.9-to-v0.10.md)
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
- The candidate awaits human review. It has not been pushed, tagged, or
  published; the documentation and focused tests are not final release-gate
  evidence.

## [0.9.1]

- Last v0.9 maintenance release.

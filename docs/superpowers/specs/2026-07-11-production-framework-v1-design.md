# Gin-Bear Production Framework Design

## Context

`gin-bear` is both a reusable Gin runtime and a project scaffolding CLI. The
repository currently exposes 200+ public Go symbols and is already consumed by
generated projects, so production hardening must not require existing users to
rewrite their applications.

The current baseline is `v0.9.1-18-g500c092`. The ordinary and race test suites
are red because `scripts/release_check_test.go` still requires the removed
Dependabot configuration. Core package statement coverage is 46.5%, and total
repository coverage is 44.4%.

The comparison set is deliberately mixed:

- [go-blueprint](https://github.com/Melkeydev/go-blueprint) demonstrates a
  single CLI, generated-project integration tests, release automation, and
  optional features without copying the generator repository into every app.
- [go-nunu](https://github.com/go-nunu/nunu) demonstrates a Gin-oriented
  layered application layout and explicit command boundaries.
- [go-clean-template](https://github.com/evrone/go-clean-template) demonstrates
  clear domain boundaries, unit and integration tests, linting, migrations,
  vulnerability checks, and generated mocks.
- [go-starter](https://github.com/allaboutapps/go-starter) demonstrates
  contract-first API generation, isolated database tests, coverage reporting,
  changelogs, and an explicit upgrade path for generated applications.

The objective is not to copy all their features. It is to adopt the production
properties that reduce runtime risk and long-term upgrade cost.

## Product Position

`gin-bear` will be a **lightweight production Gin framework core plus a project
generation CLI**.

The supported core is:

- application lifecycle and graceful shutdown;
- configuration loading and validation;
- application-scoped dependency registration;
- HTTP routing, binding, validation, and error responses;
- extension points for authentication and authorization;
- optional GORM and Redis adapters;
- health, metrics, tracing, migration, cron, WebSocket, and OpenAPI support;
- a CLI that generates compiling, testable applications and resource modules.

The following surfaces are compatibility-only or experimental in the v0 line:

- disabled MQ, Kafka, RocketMQ, Pulsar, WAF, GeoIP, BigQuery, schema, config
  center, circuit breaker, and ID-generator configuration;
- dynamic Go plugins;
- the incomplete gRPC runtime.

They will not be expanded during this work. Existing exported types and methods
remain available, receive deprecation or experimental documentation, and may
only be removed in a future `v2` module with a migration guide.

## Compatibility Contract

1. The module path remains `github.com/duiniwukenaihe/gin-bear`.
2. Existing valid `pkg/bear` calls continue to compile throughout the v0 line.
3. Existing YAML keys continue to parse; legacy no-op keys emit one startup
   warning when enabled.
4. Existing generated projects are not rewritten. CLI changes affect only new
   generation or an explicit regeneration command.
5. Security fixes may make invalid or unsafe configurations fail at startup.
   Every such change requires a documented migration note.
6. Legacy package-level helpers remain facades over the default application.
   New runtime code uses application-scoped dependencies.
7. No Docker or Kubernetes delivery assets are introduced.

## Confirmed Gaps

### Release Baseline

- `go test ./...` and `go test -race ./...` fail on the stale Dependabot test.
- CI is a single opaque script job and installs `govulncheck@latest`, so tooling
  is not reproducible.
- There is no compatibility gate, coverage floor, changelog, or CLI release
  workflow despite the published `v0.9.1` tag.

### Scaffolding

- Two independent `bear` CLIs implement conflicting commands and templates.
- One `new` command clones and copies the entire framework repository; the
  other generates a `go.mod` without adding the framework dependency.
- Generated tests can be named `test_example.go` instead of `*_test.go`.
- There is no end-to-end assertion that a newly generated application builds,
  starts, exposes health endpoints, and shuts down.

### Runtime And Lifecycle

- IoC, metrics, error registry, logger, Gin mode, and OpenTelemetry defaults use
  package-global mutable state. Tests manually replace private globals.
- bean initialization and shutdown order comes from map iteration and is not
  deterministic.
- HTTP starts before all listeners are proven ready. A later gRPC bind failure
  can return while the HTTP goroutine remains alive.
- gRPC has no public service registration API and graceful stop has no deadline
  fallback.
- shutdown hooks cannot return errors or receive the shutdown context.

### HTTP Contract

- the handler cache uses a function code pointer as its key; bound methods from
  different instances can reuse the first instance's closure;
- the claimed handler warm-up passes a `reflect.Type` to the converter and does
  not warm actual handlers;
- unsupported handler inputs silently receive zero values;
- JSON bodies are decoded without an application limit or trailing-value check;
- Fairing and controller-interceptor failures are always returned as HTTP 400,
  even for authentication, authorization, and internal errors;
- request IDs trust arbitrary client values without length or character limits.

### Security And Data Correctness

- omitting `trusted_proxies` leaves Gin's permissive proxy trust behavior in
  place, allowing spoofed client IPs in logs and IP-based rate limits;
- traces record raw query strings, which can contain credentials or personal
  data;
- production metrics and detailed readiness errors are public by default;
- `AuthTokenManager` dereferences Redis even when token revocation storage is
  unavailable;
- audit context extraction uses unchecked string assertions and can panic;
- MySQL and PostgreSQL TLS configuration share the misleading `sslmode` field;
- distributed cron locks use a shared value and unconditional `DEL`, so a job
  that outlives its TTL can delete another worker's lock.

### Maintenance

- configuration structs advertise many disabled capabilities as if supported;
- YAML and JSON parsing accepts unknown fields, and file parse failures can be
  logged and ignored instead of preventing startup;
- the custom metrics registry is global and lacks process/runtime collectors;
- OpenAPI generation is best-effort but is not validated as an API contract;
- critical packages have less than 50% statement coverage.

## Architecture

### Application Runtime

`Bear` owns a runtime object containing configuration, logger, container,
metrics registry, and lifecycle registry. Middleware and controllers capture
that runtime instead of resolving package globals on every request. Existing
`GetInjector`, `GetByType`, and logging helpers delegate to the most recently
created default application for compatibility.

Lifecycle registration records insertion order. Initialization runs in that
order; shutdown runs in reverse order with a shared bounded context. All
listeners bind before serving starts. HTTP and optional servers report fatal
serve errors through one coordinated error group. Graceful shutdown has a hard
deadline and returns joined errors.

### HTTP Pipeline

Handlers are compiled once when a route is registered. Each compiled adapter
captures the actual function value, validates the signature, and has an
explicit binding plan. There is no process-global function-pointer cache.

All request failures use one error writer. `BearError` controls the HTTP status,
stable application code, public message key, and wrapped internal cause.
Unexpected causes are logged with request and trace identifiers but are never
sent to clients.

The server applies explicit header and body limits. JSON decoding rejects a
second top-level value. Request IDs are accepted only when bounded and valid;
otherwise the framework generates one. Trusted proxy behavior defaults to no
trusted proxy and must be explicitly configured.

### Optional Dependencies

Redis-backed token revocation and rate limiting declare their failure policy.
JWT validation remains available without Redis. Database TLS settings are
driver-specific and validated. Distributed cron jobs use unique lock ownership
and compare-and-delete release semantics.

### Observability

Each application receives an isolated Prometheus registry with HTTP and Go
runtime collectors. Metrics keep existing `gin_bear_*` names where compatible.
Traces exclude raw query strings and include service version and request ID.
Readiness checks run concurrently under bounded contexts and expose sanitized
public results while logging detailed causes.

### CLI And Generated Applications

Both executable paths become thin wrappers around one internal Cobra command
tree. The legacy path remains buildable. `bear new` renders an embedded,
versioned template and writes a normal dependency on `gin-bear`; it never clones
the framework repository. Generated applications use `cmd/server` and
`internal/<resource>` boundaries and include unit and startup smoke tests.

## Testing Strategy

- regression tests first for every confirmed defect;
- unit tests for config, handler compilation, error mapping, lifecycle order,
  lock ownership, and telemetry sanitization;
- generated-project golden tests plus compile, test, startup, health, and
  shutdown smoke tests in temporary directories;
- SQLite for portable migration/repository tests and `miniredis` for Redis
  protocol behavior, with no container requirement;
- `go test`, race tests, vet, pinned vulnerability scanning, static analysis,
  module tidiness, API compatibility checks, and a core coverage floor in CI;
- final release verification from a clean worktree on macOS/Linux-compatible
  Go code paths.

## Delivery Sequence

The work ships as one coordinated production milestone but remains reviewable
through focused Chinese commits:

1. restore a green, reproducible baseline;
2. lock compatibility and supported scope;
3. unify CLI and prove generated projects;
4. isolate runtime state and fix lifecycle;
5. fix handlers, binding, and errors;
6. harden HTTP, authentication, and proxy behavior;
7. fix database, Redis, and cron correctness;
8. standardize observability and API contracts;
9. run the full quality gate and publish `v0.10.0-rc.1` documentation.

`v1.0.0` is considered only after the release candidate is used by at least one
existing generated project and one newly generated project without an
undocumented migration.

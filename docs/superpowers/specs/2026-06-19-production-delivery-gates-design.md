# Production Delivery Gates Design

**Goal:** Strengthen delivery confidence by adding CI quality gates and removing small deprecated code paths that make the scaffold look unfinished.

**Scope:** This round focuses on repository-level verification and low-risk CLI cleanup. Runtime observability, tracing, migrations, and OpenAPI quality remain separate follow-up work.

## Approach

CI should prove more than basic compilation:

- Keep module tidiness, test, vet, and Docker build checks.
- Add race-enabled tests so concurrent middleware, route cache, limiter, and generated-code paths have a stronger regression gate.
- Add `govulncheck` so known vulnerable dependency and standard-library usage is caught during pull requests.
- Add explicit binary builds for all command packages so release entry points are validated directly.

The old `cmd/bear` generator still uses deprecated `strings.Title`. Replace it with a small local ASCII-safe helper that preserves the existing command output shape for common scaffold names. Add a focused unit test so the helper does not regress.

## Testing

- Add tests for the old CLI title helper.
- Run package and full-repository tests.
- Run `go test -race ./...`.
- Run `go vet ./...`.
- Run `govulncheck ./...` if the tool can be installed in the current environment.
- Attempt a local Docker build; if Docker is unavailable, report that as an environment limitation.

## Compatibility

The CI workflow becomes stricter but does not change public runtime APIs. The title helper keeps old behavior for simple English names and improves hyphen/underscore scaffold inputs.

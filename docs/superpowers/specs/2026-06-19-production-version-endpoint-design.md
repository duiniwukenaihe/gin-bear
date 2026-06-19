# Production Version Endpoint Design

**Goal:** Expose build and release identity at runtime so operators can confirm which binary is running in production.

**Scope:** This round adds a lightweight `/version` endpoint and ldflags-compatible build variables. It does not add release automation, changelog generation, SBOM publishing, or artifact signing.

## Approach

Add package-level build variables in `pkg/bear`:

- `Version`
- `Commit`
- `BuildTime`

`GetBuildInfo()` returns those fields plus the Go runtime version and platform. Defaults are safe values such as `dev` and `unknown`.

`HealthController` registers `/version` alongside `/health`, `/live`, and `/ready`. This keeps release identity close to the operational endpoints and ensures it is enabled by the existing `EnableHealth()` pattern.

The root Dockerfile and old generated-project Dockerfile should pass these values through `-ldflags -X`, using build args. Documentation should show equivalent local build commands.

## Testing

Tests should temporarily set the build variables, call `EnableHealth()`, request `/version`, and assert that the JSON includes injected values and non-empty Go runtime metadata.

## Compatibility

Existing applications are unaffected except that `EnableHealth()` now exposes an additional `/version` endpoint. The endpoint is read-only and does not expose secrets.

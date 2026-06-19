# Production Hardening Batch 3 Design

## Goal

Raise the scaffold from a production baseline toward a more complete production operating model by closing the highest remaining gaps in one batch instead of shipping tiny isolated fixes.

## Scope

This batch covers four production areas:

- HTTP distributed tracing with OpenTelemetry.
- Explicit SQL migration rollback and execution locking.
- OpenAPI security scheme documentation for JWT-protected APIs.
- Release and supply-chain delivery helpers for repeatable artifact generation.

The batch does not attempt to add database span instrumentation, a full release management platform, or database-specific advisory locks. Those remain follow-up improvements once the core APIs are stable.

## Tracing Design

`EnableTracing(ctx)` should become functional. When tracing is enabled, it initializes an OpenTelemetry tracer provider, configures W3C trace-context propagation, registers an HTTP middleware, and adds a shutdown hook to flush spans.

The HTTP middleware should:

- Extract inbound `traceparent` headers.
- Create a server span for each request.
- Use the Gin route pattern as the span name when available.
- Record method, route, target, status code, request id, client IP, and error status.
- Keep tracing disabled when config says `enabled: false`.

Supported exporters are `stdout`, `otlp`, and `none`. `none` is useful for tests and for enabling propagation without exporting.

## Migration Design

The migration runner already supports idempotent `Up`. This batch adds:

- A lock table that prevents concurrent migration runners from applying changes at the same time.
- `Down(ctx, migrations, steps)` to roll back the latest applied migrations using loaded down SQL.
- Transactional deletion from `schema_migrations` only after down SQL succeeds.

The lock should be implemented with a portable table insert/delete approach so SQLite tests and common SQL databases share the same behavior.

## OpenAPI Design

Generated OpenAPI should document JWT bearer authentication when auth config is present. The document should add `components.securitySchemes.BearerAuth` and attach top-level `security` so API consumers can see that endpoints are protected by default. Public path exceptions remain runtime behavior and are documented in production docs rather than fully modeled per route in this batch.

## Release Design

Add a lightweight `scripts/release-check.sh` that runs the same local release gates documented in production docs, including build, tests, race tests, vet, govulncheck, module tidiness, and optional SBOM generation when `syft` is available.

CI should invoke this script so local and remote release checks stay aligned. Docker build remains in CI as a separate step because local Docker may be unavailable.

## Testing

Add focused tests for:

- HTTP tracing span creation and inbound trace context propagation.
- Migration rollback and lock cleanup.
- OpenAPI JWT security scheme generation.
- Release script existence, executable bit, and CI invocation.

Run the full production verification suite before committing.

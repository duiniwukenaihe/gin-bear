# Production Hardening Batch 5 Design

## Goal

Continue moving the scaffold toward a stronger production posture by improving API contract fidelity, migration safety, container metadata, dependency maintenance, and operational documentation.

## Scope

This batch covers:

- Standard OpenAPI error response schemas.
- SQL identifier validation for migration table names.
- Docker OCI labels and container healthcheck.
- Dependabot configuration for Go modules, GitHub Actions, and Docker.
- Security policy and production runbook documentation.

## OpenAPI Error Contracts

Generated OpenAPI should include reusable `ErrorResponse` schema and common non-2xx responses. All operations should document `400` and `500`; protected operations should also document `401`. Public operations with `security: []` should not add `401`.

## Migration Table Name Safety

Migration runner currently formats table names into SQL statements. Custom `Table` and `LockTable` values must be validated as simple SQL identifiers before use. This prevents accidental malformed SQL and avoids turning configurable table names into an injection surface.

## Container Metadata

Root and generated Dockerfiles should include OCI labels for source, revision, version, and creation time. Runtime image should also expose a `HEALTHCHECK` against `/live` so container runtimes can detect failed processes.

## Dependency And Operations Hygiene

Add Dependabot updates for Go modules, GitHub Actions, and Docker. Add `SECURITY.md` for vulnerability reporting expectations and `docs/runbook.md` for common production operations: deploy checks, rollback, migration recovery, tracing/metrics, and incident response.

## Testing

Tests should assert OpenAPI response contracts, migration identifier rejection, Dockerfile labels/healthcheck, Dependabot content, security policy presence, and runbook coverage.

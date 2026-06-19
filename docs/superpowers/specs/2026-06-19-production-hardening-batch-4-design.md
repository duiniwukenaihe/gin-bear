# Production Hardening Batch 4 Design

## Goal

Continue improving production readiness after batch 3 by closing operational polish gaps that affect rollout safety, contract accuracy, and artifact hygiene.

## Scope

This batch covers:

- Runtime configuration validation beyond basic struct tags.
- OpenAPI security exceptions for configured public paths.
- Migration lock recovery for failed or interrupted deploy jobs.
- Docker build context hygiene through `.dockerignore`.
- CI SBOM artifact generation and upload.

## Configuration Validation

`SysConfig.Validate()` should validate semantic configuration, not only struct tags. It should reject invalid tracing exporters, tracing sample rates outside `0..1`, metrics paths that do not start with `/`, and malformed server timeout durations. This catches common production misconfiguration before the app starts.

## OpenAPI Security Exceptions

Batch 3 added top-level JWT security. This batch makes public path exceptions visible in generated OpenAPI operations. If a route matches `auth.public_paths`, the operation should set `security: []`, which is the OpenAPI way to override global auth for a single operation.

## Migration Lock Recovery

Batch 3 added an execution lock. This batch adds an explicit `ForceUnlock(ctx)` recovery method for deploy operators. It should ensure the lock table exists and delete the global lock row. This is intentionally explicit instead of automatic stale-lock removal so a human-controlled deploy step can decide when recovery is safe.

## Docker And Supply Chain

Add `.dockerignore` to keep `.git`, CI metadata, docs, tests, local files, and generated SBOM files out of Docker build context. The legacy `bear new` generator should also write `.dockerignore`.

Update `scripts/release-check.sh` so CI can request SBOM generation with `GENERATE_SBOM=true`; if `syft` is unavailable, the script installs it with `go install`. CI should upload `sbom.spdx.json` when present.

## Testing

Add tests for:

- Invalid semantic config values.
- OpenAPI public route security override.
- Migration `ForceUnlock`.
- Root and generated `.dockerignore` content.
- CI artifact upload and forced SBOM generation path.

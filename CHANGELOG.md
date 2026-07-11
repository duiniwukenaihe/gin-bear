# Changelog

All notable changes to gin-bear are documented in this file.

## [0.10.0] - 2026-07-11

### Added

- Production configuration loading with strict decoding, environment overrides,
  and validation errors returned by `LoadConfig`.
- Per-runtime Prometheus metrics, bounded readiness checks, tracing, and
  generated OpenAPI validation.
- A CLI-only release process for `cmd/bear` archives, SHA-256 checksums, source
  archives, and release metadata.

### Changed

- Production defaults now require explicit trusted proxies, request body
  limits, and safe configuration.
- MySQL uses `database.tls`; legacy `database.sslmode` is ignored for MySQL.
- Redis-backed token revocation reports a typed availability error when Redis
  is not configured.

### Upgrade Notes

Read [the v0.9 to v0.10 migration guide](docs/migration-v0.9-to-v0.10.md)
before deploying. It records exact behavior changes and rollback steps.

## [0.9.1]

- Last v0.9 maintenance release.

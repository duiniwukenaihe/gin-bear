# Changelog

All notable changes to gin-bear are documented in this file.

## [v0.10.0-rc.1] - Unreleased

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

### Release Candidate Verification

- Local release-candidate verification raises the repository coverage gate to
  70%. The current profile is 73.5%, and handler, binding, errors, config
  loading, lifecycle, auth, migration lock, cron lock, CLI, and scaffold
  critical chains each exceed 80%.
- A release-only end-to-end test builds both a v0.9-style fixture and a newly
  generated application, verifies health, success, validation, authorization,
  `SIGTERM` shutdown, and rejects secret values in captured logs and traces.
- The candidate awaits human review. It is not published, no release tag has
  been created, and no branch or artifact has been pushed by this verification.

## [0.9.1]

- Last v0.9 maintenance release.

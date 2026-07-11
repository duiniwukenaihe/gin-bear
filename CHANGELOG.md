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
  70%. The freshly regenerated profile is 2,822/3,723 statements (75.8%);
  complete handler and lifecycle chains are 82.9% and 84.9%, and every other
  manifest-backed critical group exceeds 80%.
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
- The fresh local RC run based on commit `1db2743e3b1146ecc6592e0ea46cfa4e5ad311c1`
  and shuffle seed `20260711` completed clean, count1, shuffle20, race3, vet,
  pinned staticcheck/govulncheck, release-check, diff, and hygiene with exit 0;
  govulncheck found zero reachable vulnerabilities and final status was
  unchanged.
- The candidate awaits human review. It is not published, no release tag has
  been created, and no branch or artifact has been pushed by this verification.

## [0.9.1]

- Last v0.9 maintenance release.

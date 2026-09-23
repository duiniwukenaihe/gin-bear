# Supported versions

Verified matrix for this checkout. Lowest supported majors are tested; newer
patch releases within a major are accepted, newer majors are not claimed
until verified. Observed local versions are logged by the test suites, not
asserted here.

| Dependency | Lowest supported | Pinned in CI | Notes |
| --- | --- | --- | --- |
| Go toolchain | 1.26.6 | `GOTOOLCHAIN=go1.26.6`, `go 1.26.6` directives | Pinned verification tools via `*_EXPECTED_SHA256` |
| PostgreSQL | 16 | `postgres:16` (compose/services) | Local acceptance observed 18.4 |
| MySQL | 8.0 | `mysql:8.0` (compose/services) | Local acceptance observed 8.4.11; NOT_RUN without a DSN |
| Redis | 7 | `redis:7` (compose/services) | Local acceptance observed 8.8.0 |
| eino (agent extension) | v0.9.19 | `go.mod` pin | Apache-2.0; see `extensions/agent/adr-001-model-integration.md` |
| MCP Go SDK (dev bridge) | v1.8.0 | `go.mod` pin | Apache-2.0/MIT history; see plan §11 |
| Generated-project Go | 1.26.6 | scaffold `go.mod` template | Matches framework toolchain |
| Production image bases | `golang:1.26.6-bookworm`, `gcr.io/distroless/static-debian12:nonroot` | `Dockerfile` template | Bump together with the `go` directive |

## Dependency rhythm

- Weekly: review `go.mod` outdated modules and the vulnerability feed;
  `govulncheck` runs in the quality gate on every change.
- Monthly: bump non-major dependencies after a green `make verify` plus
  `scripts/test-integration.sh`.
- Major upgrades (Go, PostgreSQL, MySQL, Redis, eino, MCP SDK): separate
  work item with its own acceptance evidence; never bundled with features.
- No scheduled automation is created here; cadence is operated manually
  until a dedicated maintenance work item wires it.

## Release acceptance checklist (per candidate tag)

1. `make verify` and the clean-worktree `make verify-rc` gate green on the
   exact `main` commit selected for tagging (quality, compatibility, E2E,
   repeated/shuffled tests, race, vet, staticcheck, govulncheck). The tag
   workflow reruns `make verify` before publication.
2. `BEAR_INTEGRATION_REQUIRE=pg,mysql,redis scripts/test-integration.sh`
   green with all three real engines. `NOT_RUN` is not final-release evidence.
3. Nested modules green (`tools/bear-mcp`, `extensions/agent`, incl. the
   tasks PostgreSQL variant).
4. A new app generated with the *released* CLI/framework versions (not a local
   replace) boots, migrates, and serves CRUD; previous-template preview and
   failed-upgrade recovery verified.
5. Binary distribution (checksums, SBOM, provenance) only when binaries ship;
   source releases do not change for those nouns.

Migration digest drift detection is a separate work item: history
compatibility, missing-old-SQL states, and manual verification come before any
automatic backfill.

# Production Runbook

## Release Checklist

1. Review configuration from `application-prod.yaml.example`.
2. Run the same pinned local verification command required by the README and
   security policy:

   ```bash
   GOSUMDB=sum.golang.org GOTOOLCHAIN=go1.25.12 make verify
   ```
3. Before an RC or tag, run the complete audited gate with an explicit shuffle
   seed:

   ```bash
   SHUFFLE_SEED=20260711 GOSUMDB=sum.golang.org GOTOOLCHAIN=go1.25.12 make verify-rc
   ```
4. Build the application binary with `VERSION`, `COMMIT`, and `BUILD_TIME` linker flags.
5. Confirm `/live`, `/ready`, `/version`, and `/metrics` in the target environment.
6. Confirm `server.shutdown_timeout`, `health.readiness_timeout`, `log.level`, and `redis.required` match the service's dependency profile.
7. For a tag release, confirm `.goreleaser.yml` still builds only `cmd/bear`
   archives for Linux, macOS, and Windows, plus SHA-256 checksums, source
   archive, changelog text, and release metadata.
8. Before publishing, run the pinned local snapshot check:

   ```bash
   GOSUMDB=sum.golang.org GOTOOLCHAIN=go1.25.12 go run github.com/goreleaser/goreleaser/v2@v2.17.0 release --snapshot --clean
   ```

   Verify the generated SHA-256 manifest, then inspect the CLI archives and
   release metadata:

   ```bash
   (cd dist && shasum -a 256 -c checksums.txt)
   ```

   Inspect `dist/checksums.txt`, the CLI archives, and `dist/artifacts.json`.
   Remove `dist/` after the check; snapshot artifacts are not committed.

The release workflow runs only for `v*` tags. It runs `make verify-rc` before
GoReleaser with Go 1.25.12, uploads the `rc-verification` logs even on failure,
and grants `contents: write` only to the release job.

## v0.10.0-rc.1 Candidate Audit

The `v0.10.0-rc.1` candidate is under local verification. It is not tagged or published.
All commands use `GOSUMDB=sum.golang.org` and `GOTOOLCHAIN=go1.25.12`, run in
the foreground, and complete before the next command starts.

The reproducible coverage sequence keeps its profile outside the worktree:

```bash
profile=$(mktemp /tmp/gin-bear-coverage.XXXXXX)
trap 'rm -f "$profile"' EXIT
GOSUMDB=sum.golang.org GOTOOLCHAIN=go1.25.12 go test ./... -coverprofile="$profile" -count=1
GOSUMDB=sum.golang.org GOTOOLCHAIN=go1.25.12 scripts/check-coverage.sh "$profile"
GOSUMDB=sum.golang.org GOTOOLCHAIN=go1.25.12 go tool cover -func="$profile"
```

The profile regenerated on 2026-07-11 contains 2,822 covered statements out of
3,723, or 75.799087% before display formatting (75.8%). The checked-in manifest
lists every production file in each critical group; handler includes all of
`bear.go`, `handler.go`, `responder.go`, and `fairing.go`, lifecycle includes all
of `bear.go` and `lifecycle.go`, and scaffold is checked against the current
platform's complete `internal/scaffold` production `GoFiles`. The gate reports:

```text
critical coverage handler 82.9%
critical coverage binding 88.5%
critical coverage errors 94.5%
critical coverage config-loader 91.8%
critical coverage lifecycle 84.9%
critical coverage auth 92.2%
critical coverage migration-lock 80.3%
critical coverage cron-lock 88.9%
critical coverage cli 93.1%
critical coverage scaffold 82.9%
```

Public API compatibility is checked without repository history or local tags:

```bash
GOSUMDB=sum.golang.org GOTOOLCHAIN=go1.25.12 scripts/check-api-compat.sh
```

The committed `scripts/api/v0.9.1.txt` module manifest covers every public Go
package from v0.9.1. The pinned official `apidiff` gate permits additions and
rejects removals or incompatible changes; the separate v0.9 consumer fixture is
also compiled by `go test ./...`.

Run the release-only compatibility test once with:

```bash
GOSUMDB=sum.golang.org GOTOOLCHAIN=go1.25.12 BEAR_RELEASE_E2E=1 go test ./scripts/releasee2e -run '^TestReleaseCandidateApplications$' -count=1 -v
```

It builds a v0.9-style application and a newly generated application in
temporary directories. Each fixture must start, answer `/live`, `/ready`, a
successful route, a validation failure, and an unauthorized request, then exit
cleanly after `SIGTERM`. Captured logs and stdout traces are rejected if they
contain the request secret.

After coverage and compatibility pass, run the final gate through its tracked
entry point. It records commit `1db2743e3b1146ecc6592e0ea46cfa4e5ad311c1` as
the base HEAD for this review worktree, Go and pinned tool versions, shuffle
seed `20260711`, each command and exit code, and before/after worktree status:

```bash
SHUFFLE_SEED=20260711 GOSUMDB=sum.golang.org GOTOOLCHAIN=go1.25.12 make verify-rc
```

By default logs are retained in a `mktemp` directory outside the repository;
CI sets `RC_ARTIFACT_DIR` under `runner.temp` and uploads it. The final hygiene
step permits the active local `codex/production-framework-v010` development
branch, limits remote heads to `main` and `codex/production-baseline`, rejects
container/Kubernetes/Helm files and `coverage.out`, and requires final worktree
status to equal its initial snapshot.

### Recorded Result: 2026-07-11

The final fresh run used base HEAD
`1db2743e3b1146ecc6592e0ea46cfa4e5ad311c1`, shuffle seed `20260711`, Go
1.25.12, staticcheck 2026.1 (`v0.7.0`), and govulncheck `v1.6.0`. Its audit
directory was
`/var/folders/n0/1dxtrxzn305_fjv9vgth7pf40000gn/T/gin-bear-rc.H44w3t`.
`results.tsv` recorded exit code 0 for `clean`, `count1`, `shuffle20`, `race3`,
`vet`, `staticcheck`, `govulncheck`, `release-check`, `diff-check`, `hygiene`,
and `final`.

Govulncheck reported zero reachable vulnerabilities. It also reported two
vulnerabilities in imported packages and twenty in required modules that this
code does not call. The macOS race link printed `malformed LC_DYSYMTAB`
warnings, but every affected command exited 0. The before/after status snapshots
were byte-identical, the release-owned coverage profile was removed, and no
worktree `coverage.out` remained. Remote heads were only `main` and
`codex/production-baseline`; the active local development branch remained
allowed. Human review is still required: this candidate is Unreleased, and no
tag, push, merge, or release was performed.

## Rollback

1. Stop routing new traffic to the unhealthy version.
2. Roll back to the previous application binary or release artifact.
3. Check `/ready` before restoring traffic.
4. Review `/version` to confirm the running commit.
5. For framework behavior changes, follow the exact configuration and rollback
   sequence in [the v0.9 to v0.10 migration guide](migration-v0.9-to-v0.10.md).

## Migration Recovery

Run migrations as a separate deploy step before starting the new app version. If a migration job is interrupted while holding the migration lock, verify no migration process is still running, then call `MigrationRunner.ForceUnlock(ctx)` from an admin command before retrying.

Use `MigrationRunner.Down(ctx, migrations, steps)` only for reviewed rollback SQL. Prefer forward fixes when data loss is possible.

## Observability

Use `/metrics` for Prometheus scraping and `EnableTracing(ctx)` with OTLP for distributed tracing. Request logs include request id and latency information. During incidents, correlate `/version`, trace ids, request ids, and deployment timestamps.

For deeper framework diagnostics, temporarily raise `LOG_LEVEL=debug` and restore it after the incident.

## Incident Response

1. Identify blast radius from health checks, metrics, traces, and logs.
2. Decide whether to roll back, fail over, or disable traffic.
3. Preserve logs, trace ids, release metadata, and migration history.
4. File follow-up work for missing alerts, missing dashboards, or unclear runbook steps.

# Production Runbook

## Release Checklist

1. Review configuration from `application-prod.yaml.example`.
2. Run the same pinned local verification command required by the README and
   security policy:

   ```bash
   GOSUMDB=sum.golang.org GOTOOLCHAIN=go1.25.12 make verify
   ```
3. Build the application binary with `VERSION`, `COMMIT`, and `BUILD_TIME` linker flags.
4. Confirm `/live`, `/ready`, `/version`, and `/metrics` in the target environment.
5. Confirm `server.shutdown_timeout`, `health.readiness_timeout`, `log.level`, and `redis.required` match the service's dependency profile.
6. For a tag release, confirm `.goreleaser.yml` still builds only `cmd/bear`
   archives for Linux, macOS, and Windows, plus SHA-256 checksums, source
   archive, changelog text, and release metadata.
7. Before publishing, run the pinned local snapshot check:

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

The release workflow runs only for `v*` tags. It runs `make verify` before
GoReleaser with Go 1.25.12 and grants `contents: write` only to the release job.

## v0.10.0-rc.1 Candidate Audit

The `v0.10.0-rc.1` candidate is under local verification. It is not tagged or published.
All commands use `GOSUMDB=sum.golang.org` and `GOTOOLCHAIN=go1.25.12`, run in
the foreground, and complete before the next command starts.

The reproducible coverage sequence is:

```bash
rm -f coverage.out
GOSUMDB=sum.golang.org GOTOOLCHAIN=go1.25.12 go test ./... -coverprofile=coverage.out -count=1
GOSUMDB=sum.golang.org GOTOOLCHAIN=go1.25.12 scripts/check-coverage.sh coverage.out
GOSUMDB=sum.golang.org GOTOOLCHAIN=go1.25.12 go tool cover -func=coverage.out
```

The 2026-07-11 local profile reports 73.5% total statement coverage. The
critical-chain gate reports:

```text
critical coverage handler 82.0%
critical coverage binding 87.9%
critical coverage errors 94.5%
critical coverage config-loader 91.8%
critical coverage lifecycle 85.0%
critical coverage auth 92.2%
critical coverage migration-lock 80.3%
critical coverage cron-lock 88.9%
critical coverage cli 93.1%
critical coverage scaffold 82.9%
```

Run the release-only compatibility test once with:

```bash
GOSUMDB=sum.golang.org GOTOOLCHAIN=go1.25.12 BEAR_RELEASE_E2E=1 go test ./scripts/releasee2e -run '^TestReleaseCandidateApplications$' -count=1 -v
```

It builds a v0.9-style application and a newly generated application in
temporary directories. Each fixture must start, answer `/live`, `/ready`, a
successful route, a validation failure, and an unauthorized request, then exit
cleanly after `SIGTERM`. Captured logs and stdout traces are rejected if they
contain the request secret.

After coverage and compatibility pass, run the final gate strictly in this
order and record every exit code:

```bash
GOSUMDB=sum.golang.org GOTOOLCHAIN=go1.25.12 go clean -testcache
GOSUMDB=sum.golang.org GOTOOLCHAIN=go1.25.12 go test ./... -count=1
GOSUMDB=sum.golang.org GOTOOLCHAIN=go1.25.12 go test ./... -shuffle=on -count=20
GOSUMDB=sum.golang.org GOTOOLCHAIN=go1.25.12 go test -race ./... -count=3
GOSUMDB=sum.golang.org GOTOOLCHAIN=go1.25.12 go vet ./...
GOSUMDB=sum.golang.org GOTOOLCHAIN=go1.25.12 go run honnef.co/go/tools/cmd/staticcheck@v0.7.0 ./...
GOSUMDB=sum.golang.org GOTOOLCHAIN=go1.25.12 go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...
GOSUMDB=sum.golang.org GOTOOLCHAIN=go1.25.12 scripts/release-check.sh
git diff --check
```

Before review, inspect local branches, remote heads, and container assets. The
active local `codex/production-framework-v010` development branch is allowed;
remote heads are limited to `main` and `codex/production-baseline`. Docker,
Compose, Kubernetes, and Helm files must remain absent. Remove `coverage.out`
and generated snapshot artifacts before committing. Tagging and pushing remain
manual post-review steps.

### Recorded Result: 2026-07-11

The final local sequence completed with exit code 0 for clean, the single test
run, `-shuffle=on -count=20`, `-race -count=3`, vet, pinned staticcheck, pinned
govulncheck, the explicit 70% release check, and the default release check.
`govulncheck` reported zero reachable vulnerabilities. The macOS race linker
printed the known `malformed LC_DYSYMTAB` warning, but the race command exited 0.

During verification, the first shuffle run hit the package's 10-minute timeout
because generated-resource compilation was repeated 20 times. That compile
check now runs once in the release-only E2E while normal scaffold tests retain
generation, formatting, and atomicity checks. A later diagnostic full shuffle
run exited 1 under concurrent package load; its recorded seed passed a focused
`pkg/bear -count=20` reproduction, and the subsequent full required command
completed with exit 0. Treat the remaining timing headroom in scaffold shuffle
tests as an RC risk and keep the final command in pre-release verification.

Repository hygiene also passed: local branches were `main`,
`codex/production-baseline`, and the active allowed development branch;
remote heads were only `main` and `codex/production-baseline`; the container and
Kubernetes file search returned no results. Human review is still required
before creating any tag, push, merge, or release.

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

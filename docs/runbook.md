# Production Runbook

## Release Checklist

1. Review configuration from `application-prod.yaml.example`.
2. Run `GOPROXY=https://goproxy.cn,direct scripts/release-check.sh`.
3. Build the application binary with `VERSION`, `COMMIT`, and `BUILD_TIME` linker flags.
4. Confirm `/live`, `/ready`, `/version`, and `/metrics` in the target environment.
5. Confirm `server.shutdown_timeout`, `health.readiness_timeout`, `log.level`, and `redis.required` match the service's dependency profile.
6. For a tag release, confirm `.goreleaser.yml` still builds only `cmd/bear`
   archives for Linux, macOS, and Windows, plus SHA-256 checksums, source
   archive, changelog text, and release metadata.
7. Before publishing, run the pinned local snapshot check:

   ```bash
   go run github.com/goreleaser/goreleaser/v2@v2.17.0 release --snapshot --clean
   ```

   Inspect `dist/checksums.txt`, the CLI archives, and `dist/artifacts.json`.
   Remove `dist/` after the check; snapshot artifacts are not committed.

The release workflow runs only for `v*` tags. It runs `make verify` before
GoReleaser and grants `contents: write` only to the release job.

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

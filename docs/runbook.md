# Production Runbook

## Release Checklist

1. Review configuration from `application-prod.yaml.example`.
2. Run `GOPROXY=https://goproxy.cn,direct scripts/release-check.sh`.
3. Build the container with `VERSION`, `COMMIT`, and `BUILD_TIME` build args.
4. Confirm `/live`, `/ready`, `/version`, and `/metrics` in the target environment.
5. Keep the generated `sbom.spdx.json` with release artifacts when CI produces one.

## Rollback

1. Stop routing new traffic to the unhealthy version.
2. Roll back to the previous image tag.
3. Check `/ready` before restoring traffic.
4. Review `/version` to confirm the running commit.

## Migration Recovery

Run migrations as a separate deploy step before starting the new app version. If a migration job is interrupted while holding the migration lock, verify no migration process is still running, then call `MigrationRunner.ForceUnlock(ctx)` from an admin command before retrying.

Use `MigrationRunner.Down(ctx, migrations, steps)` only for reviewed rollback SQL. Prefer forward fixes when data loss is possible.

## Observability

Use `/metrics` for Prometheus scraping and `EnableTracing(ctx)` with OTLP for distributed tracing. Request logs include request id and latency information. During incidents, correlate `/version`, trace ids, request ids, and deployment timestamps.

## Incident Response

1. Identify blast radius from health checks, metrics, traces, and logs.
2. Decide whether to roll back, fail over, or disable traffic.
3. Preserve logs, trace ids, release metadata, and migration history.
4. File follow-up work for missing alerts, missing dashboards, or unclear runbook steps.

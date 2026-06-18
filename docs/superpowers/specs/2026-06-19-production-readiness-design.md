# Production Readiness Design

Date: 2026-06-19
Scope: P1/P2 production readiness work after the production baseline patch.

## Goal

Make gin-bear practical to run, ship, and generate production-oriented projects. This pass extends the baseline hardening with deployment artifacts, runtime checks, stricter production security, and generated-template alignment.

## Approach

Continue on the existing production baseline branch with small compatible changes:

1. Fix repository ignore rules so source paths named `bear` are not accidentally ignored.
2. Add production configuration examples for environment variables and YAML.
3. Configure Gin runtime mode and trusted proxies from `SysConfig`.
4. Split health endpoints into liveness and readiness checks.
5. Add Docker, Compose, and GitHub Actions CI artifacts.
6. Reject weak JWT secrets in production.
7. Replace hard-coded auth skip rules with configurable public path patterns.
8. Align generated DTO validation tags with Gin binding behavior.
9. Add database readiness and optional slow SQL logging configuration.
10. Upgrade CLI-created projects to include production-oriented defaults.

## Runtime Behavior

`Ignite` remains the main entry point. It will configure Gin mode, trusted proxies, and production security validation before serving routes. Existing applications that do not set production mode keep developer-friendly defaults.

`EnableHealth` will register:

- `/health`: backward-compatible alias.
- `/live`: process liveness.
- `/ready`: dependency readiness.

Readiness checks use lightweight interfaces so DB and Redis adapters can participate without introducing heavyweight coupling.

## Security

Production mode is detected from `server.mode`, `BEAR_ENV=prod|production`, or `GIN_MODE=release`.

In production:

- weak JWT defaults such as `bear-secret` and `your-secret-key` are rejected.
- auth public paths are read from config rather than embedded demo strings.
- trusted proxies are applied when configured.

## Deployment

Add:

- `.env.example`
- `application-prod.yaml.example`
- `Dockerfile`
- `docker-compose.yml`
- `.github/workflows/ci.yml`

These artifacts should be minimal, readable, and useful for both the framework repository and generated app users.

## Generated Project Experience

`bear new` should generate a project that can be run locally, configured for production, and containerized with the same baseline defaults. Code generation should use `binding` tags so validation works with the framework's Gin binding path.

## Testing

Add or extend Go tests for:

- production weak JWT secret rejection.
- Gin mode/proxy runtime setup.
- readiness endpoint success and failure.
- auth public path config.
- CORS and error baseline regression coverage remains green.

Deployment artifacts and examples are verified by full `go test ./...`, `go vet ./...`, and basic file presence/content review.

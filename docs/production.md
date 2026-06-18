# Production Guide

## Runtime

Use production mode in deployed environments:

```bash
export BEAR_ENV=prod
export GIN_MODE=release
export JWT_SECRET="$(openssl rand -base64 48)"
```

`server.mode: release` in YAML has the same effect as `GIN_MODE=release`.

## Configuration

Start from `application-prod.yaml.example` and keep real secrets outside git. Supported environment overrides include:

- `BEAR_SERVER_PORT`
- `JWT_SECRET`
- `REDIS_ADDR`
- `POSTGRES_HOST`
- `POSTGRES_PORT`
- `POSTGRES_USER`
- `POSTGRES_PASSWORD`
- `POSTGRES_DB`

## Health Checks

- `/live` confirms the process is alive.
- `/ready` confirms registered dependencies are ready.
- `/health` remains as a backward-compatible liveness alias.

Use `/ready` for load balancer readiness and rollout gates.

## Database Migrations

Do not run implicit schema migrations from request-serving code. Keep schema changes explicit and reviewed:

1. Generate SQL migrations with your migration tool of choice.
2. Review migration SQL in code review.
3. Run migrations as a separate deploy step before starting the new app version.
4. Keep application startup limited to connection and readiness checks.

`database.slow_query_threshold` enables GORM slow query logging for runtime visibility.

## Containers

Build the app image:

```bash
docker build -t gin-bear .
```

Run local dependencies:

```bash
docker compose up --build
```

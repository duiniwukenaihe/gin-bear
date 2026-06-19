# Production SQL Migrations Design

**Goal:** Add a small SQL migration runner so schema changes can be executed as an explicit deployment step instead of hidden application startup behavior.

**Scope:** This round provides file loading and an `Up` runner. It does not add a full CLI, online schema change orchestration, database-specific locking, or automatic rollback on deploy.

## Approach

Production services should not run implicit `AutoMigrate` during request-serving startup. The framework will provide a lightweight runner that teams can call from a deploy job, admin command, or one-off script.

Migration files use a predictable pair format:

- `001_create_users.up.sql`
- `001_create_users.down.sql`

`LoadSQLMigrations(dir)` reads migration files, groups them by version/name, and sorts them lexicographically by version. Down files are optional in this round but are preserved on the `Migration` struct for future rollback support.

`MigrationRunner.Up(ctx, migrations)`:

- creates `schema_migrations` if missing,
- skips already applied versions,
- executes each migration in a transaction,
- records `version`, `name`, and `applied_at`,
- stops on the first error.

The runner accepts `*sql.DB` so it can work with the existing `GormAdapter` through `adapter.DB.DB()` and also with standalone database handles.

## Testing

Tests should use an in-memory SQLite database through the existing GORM sqlite test setup. They should verify loading/sorting from files, applying migrations, idempotent re-run behavior, and stopping on invalid SQL.

## Compatibility

No runtime startup behavior changes. Existing applications are unaffected unless they opt into the migration runner.

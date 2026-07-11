package bear

import (
	"context"
	"strings"
	"testing"
)

func TestMigrationDialectRebindsRunnerQueries(t *testing.T) {
	queries := map[string]string{
		"acquire":        "INSERT INTO schema_migration_locks (name, owner) VALUES (?, ?)",
		"release":        "DELETE FROM schema_migration_locks WHERE name = ? AND owner = ?",
		"history lookup": "SELECT version FROM schema_migrations WHERE version = ?",
		"history insert": "INSERT INTO schema_migrations (version, name) VALUES (?, ?)",
		"history latest": "SELECT version, name FROM schema_migrations ORDER BY version DESC LIMIT ?",
		"history delete": "DELETE FROM schema_migrations WHERE version = ?",
		"force lookup":   "SELECT owner FROM schema_migration_locks WHERE name = ?",
		"force delete":   "DELETE FROM schema_migration_locks WHERE name = ? AND owner = ?",
	}

	tests := []struct {
		name    string
		dialect MigrationDialect
		want    map[string]string
	}{
		{
			name:    "sqlite",
			dialect: MigrationDialectSQLite,
			want:    queries,
		},
		{
			name:    "mysql",
			dialect: MigrationDialectMySQL,
			want:    queries,
		},
		{
			name:    "postgresql",
			dialect: MigrationDialectPostgreSQL,
			want: map[string]string{
				"acquire":        "INSERT INTO schema_migration_locks (name, owner) VALUES ($1, $2)",
				"release":        "DELETE FROM schema_migration_locks WHERE name = $1 AND owner = $2",
				"history lookup": "SELECT version FROM schema_migrations WHERE version = $1",
				"history insert": "INSERT INTO schema_migrations (version, name) VALUES ($1, $2)",
				"history latest": "SELECT version, name FROM schema_migrations ORDER BY version DESC LIMIT $1",
				"history delete": "DELETE FROM schema_migrations WHERE version = $1",
				"force lookup":   "SELECT owner FROM schema_migration_locks WHERE name = $1",
				"force delete":   "DELETE FROM schema_migration_locks WHERE name = $1 AND owner = $2",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for name, query := range queries {
				got, err := tt.dialect.Rebind(query)
				if err != nil {
					t.Fatalf("Rebind(%s): %v", name, err)
				}
				if got != tt.want[name] {
					t.Fatalf("Rebind(%s) = %q, want %q", name, got, tt.want[name])
				}
			}
		})
	}
}

func TestMigrationLockOwnerPreventsStaleReleaseDeletingReplacement(t *testing.T) {
	db := newMigrationTestDB(t)
	runner := NewMigrationRunner(db, MigrationDialectSQLite)
	ctx := context.Background()
	if err := runner.ensureLockTable(ctx, defaultMigrationLockTable); err != nil {
		t.Fatalf("ensure lock table: %v", err)
	}

	firstOwner, err := runner.acquireLock(ctx, defaultMigrationLockTable)
	if err != nil {
		t.Fatalf("acquire first lock: %v", err)
	}
	if len(firstOwner) != 64 {
		t.Fatalf("first owner token length = %d, want 64 hex characters", len(firstOwner))
	}
	if err := runner.ForceUnlock(ctx); err != nil {
		t.Fatalf("force unlock first owner: %v", err)
	}
	secondOwner, err := runner.acquireLock(ctx, defaultMigrationLockTable)
	if err != nil {
		t.Fatalf("acquire replacement lock: %v", err)
	}
	if firstOwner == secondOwner {
		t.Fatal("replacement lock reused owner token")
	}

	err = runner.releaseLock(ctx, defaultMigrationLockTable, firstOwner)
	if err == nil || !strings.Contains(err.Error(), "owner") {
		t.Fatalf("stale release error = %v, want owner mismatch", err)
	}

	var storedOwner string
	if err := db.QueryRowContext(ctx,
		"SELECT owner FROM schema_migration_locks WHERE name = ?",
		defaultMigrationLockName,
	).Scan(&storedOwner); err != nil {
		t.Fatalf("read replacement lock: %v", err)
	}
	if storedOwner != secondOwner {
		t.Fatalf("stored owner = %q, want replacement owner %q", storedOwner, secondOwner)
	}
}

func TestMigrationDialectRejectsUnknownValue(t *testing.T) {
	if _, err := MigrationDialect("oracle").Rebind("SELECT ?"); err == nil {
		t.Fatal("unknown migration dialect should fail")
	}
}

func TestEnsureLockTableUpgradesLegacySchemaWithOwner(t *testing.T) {
	db := newMigrationTestDB(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `CREATE TABLE schema_migration_locks (
name TEXT PRIMARY KEY,
locked_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
)`); err != nil {
		t.Fatalf("create legacy lock table: %v", err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO schema_migration_locks (name) VALUES (?)", defaultMigrationLockName); err != nil {
		t.Fatalf("insert legacy lock: %v", err)
	}

	runner := NewMigrationRunner(db, MigrationDialectSQLite)
	if err := runner.ensureLockTable(ctx, defaultMigrationLockTable); err != nil {
		t.Fatalf("upgrade legacy lock table: %v", err)
	}
	var owner string
	if err := db.QueryRowContext(ctx,
		"SELECT owner FROM schema_migration_locks WHERE name = ?",
		defaultMigrationLockName,
	).Scan(&owner); err != nil {
		t.Fatalf("read upgraded owner: %v", err)
	}
	if owner == "" {
		t.Fatal("legacy lock owner was not populated")
	}
}

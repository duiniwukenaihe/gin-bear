package bear

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

func TestMigrationRunnerUpRejectsAppliedVersionWithDifferentName(t *testing.T) {
	db := newMigrationTestDB(t)
	runner := NewMigrationRunner(db)
	ctx := context.Background()

	applied := Migration{
		Version: "001",
		Name:    "create_users",
		UpSQL:   "CREATE TABLE migration_name_users (id INTEGER PRIMARY KEY)",
	}
	if err := runner.Up(ctx, []Migration{applied}); err != nil {
		t.Fatalf("apply original migration: %v", err)
	}

	renamed := Migration{
		Version: "001",
		Name:    "create_accounts",
		UpSQL:   "CREATE TABLE migration_name_accounts (id INTEGER PRIMARY KEY)",
	}
	err := runner.Up(ctx, []Migration{renamed})
	if err == nil {
		t.Fatal("Up() error = nil, want applied version name mismatch")
	}
	for _, detail := range []string{"001", "create_users", "create_accounts"} {
		if !strings.Contains(err.Error(), detail) {
			t.Errorf("Up() error = %q, want detail %q", err, detail)
		}
	}

	assertMigrationTableExists(t, db, "migration_name_users")
	assertMigrationTableMissing(t, db, "migration_name_accounts")
}

func TestMigrationRunnerDownValidatesEntireRollbackPlanBeforeExecutingSQL(t *testing.T) {
	tests := []struct {
		name       string
		migrations func([]Migration) []Migration
		want       []string
	}{
		{
			name: "missing migration",
			migrations: func(applied []Migration) []Migration {
				return []Migration{applied[1]}
			},
			want: []string{"001", "create_users", "not loaded"},
		},
		{
			name: "migration name mismatch",
			migrations: func(applied []Migration) []Migration {
				provided := append([]Migration(nil), applied...)
				provided[0].Name = "create_accounts"
				return provided
			},
			want: []string{"001", "create_users", "create_accounts"},
		},
		{
			name: "empty down sql",
			migrations: func(applied []Migration) []Migration {
				provided := append([]Migration(nil), applied...)
				provided[0].DownSQL = " \n\t"
				return provided
			},
			want: []string{"001", "create_users", "down sql is empty"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newMigrationTestDB(t)
			runner := NewMigrationRunner(db)
			ctx := context.Background()
			applied := []Migration{
				{
					Version: "001",
					Name:    "create_users",
					UpSQL:   "CREATE TABLE rollback_plan_users (id INTEGER PRIMARY KEY)",
					DownSQL: "DROP TABLE rollback_plan_users",
				},
				{
					Version: "002",
					Name:    "create_audit",
					UpSQL:   "CREATE TABLE rollback_plan_audit (id INTEGER PRIMARY KEY)",
					DownSQL: "DROP TABLE rollback_plan_audit",
				},
			}
			if err := runner.Up(ctx, applied); err != nil {
				t.Fatalf("apply migrations: %v", err)
			}

			err := runner.Down(ctx, tt.migrations(applied), 2)
			if err == nil {
				t.Fatal("Down() error = nil, want invalid rollback plan rejection")
			}
			for _, detail := range tt.want {
				if !strings.Contains(err.Error(), detail) {
					t.Errorf("Down() error = %q, want detail %q", err, detail)
				}
			}

			assertMigrationTableExists(t, db, "rollback_plan_users")
			assertMigrationTableExists(t, db, "rollback_plan_audit")

			var historyCount int
			if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&historyCount); err != nil {
				t.Fatalf("count migration history: %v", err)
			}
			if historyCount != 2 {
				t.Fatalf("migration history count = %d, want 2 before any rollback", historyCount)
			}
		})
	}
}

func assertMigrationTableExists(t *testing.T, db *sql.DB, table string) {
	t.Helper()
	var count int
	if err := db.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?",
		table,
	).Scan(&count); err != nil {
		t.Fatalf("look up table %s: %v", table, err)
	}
	if count != 1 {
		t.Fatalf("table %s count = %d, want 1", table, count)
	}
}

func assertMigrationTableMissing(t *testing.T, db *sql.DB, table string) {
	t.Helper()
	var count int
	if err := db.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?",
		table,
	).Scan(&count); err != nil {
		t.Fatalf("look up table %s: %v", table, err)
	}
	if count != 0 {
		t.Fatalf("table %s count = %d, want 0", table, count)
	}
}

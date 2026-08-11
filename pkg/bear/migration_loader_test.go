package bear

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExplicitMigrationRunnerRejectsMissingDatabaseWithoutPanicking(t *testing.T) {
	ctx := context.Background()
	var nilRunner *DialectMigrationRunner
	if got := nilRunner.ConfigureTables("history", "locks"); got != nil {
		t.Fatalf("nil ConfigureTables() = %#v, want nil", got)
	}
	operations := map[string]func() error{
		"up":           func() error { return nilRunner.Up(ctx, nil) },
		"down":         func() error { return nilRunner.Down(ctx, nil, 1) },
		"force unlock": func() error { return nilRunner.ForceUnlock(ctx) },
		"force state":  func() error { return nilRunner.ForceMigrationState(ctx, "001", true) },
	}
	for name, operation := range operations {
		t.Run(name, func(t *testing.T) {
			if err := operation(); err == nil || !strings.Contains(err.Error(), "requires a database") {
				t.Fatalf("operation error = %v, want missing database error", err)
			}
		})
	}

	runner := NewMigrationRunnerWithDialect(nil, MigrationDialectSQLite)
	if err := runner.Up(ctx, nil); err == nil || !strings.Contains(err.Error(), "requires a database") {
		t.Fatalf("runner.Up() error = %v, want missing database error", err)
	}
	if err := runner.Down(ctx, nil, 1); err == nil || !strings.Contains(err.Error(), "requires a database") {
		t.Fatalf("runner.Down() error = %v, want missing database error", err)
	}
	if err := runner.ForceUnlock(ctx); err == nil || !strings.Contains(err.Error(), "requires a database") {
		t.Fatalf("runner.ForceUnlock() error = %v, want missing database error", err)
	}
	if err := runner.ForceMigrationState(ctx, "001", true); err == nil || !strings.Contains(err.Error(), "requires a database") {
		t.Fatalf("runner.ForceMigrationState() error = %v, want missing database error", err)
	}
}

func TestMigrationFileParserAndLoaderIgnoreUnsupportedEntries(t *testing.T) {
	for _, name := range []string{
		"README.md",
		"001_create_users.sql",
		"001_create_users.sideways.sql",
		"missing-version.up.sql",
		"001_.up.sql",
	} {
		if _, _, _, ok := parseMigrationFileName(name); ok {
			t.Fatalf("parseMigrationFileName(%q) accepted unsupported name", name)
		}
	}

	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "002_nested.up.sql"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"README.md":                  "ignored",
		"003_rollback_only.down.sql": "DROP TABLE rollback_only",
		"004_valid.up.sql":           "CREATE TABLE valid (id INTEGER PRIMARY KEY)",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	migrations, err := LoadSQLMigrations(dir)
	if err != nil {
		t.Fatalf("LoadSQLMigrations() error = %v", err)
	}
	if len(migrations) != 1 || migrations[0].Version != "004" || migrations[0].Name != "valid" {
		t.Fatalf("LoadSQLMigrations() = %#v, want only valid up migration", migrations)
	}
}

func TestLoadSQLMigrationsRejectsDuplicateVersionAcrossMigrationNames(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
	}{
		{
			name: "up migrations",
			files: map[string]string{
				"001_create_users.up.sql": "CREATE TABLE users (id INTEGER PRIMARY KEY)",
				"001_create_posts.up.sql": "CREATE TABLE posts (id INTEGER PRIMARY KEY)",
			},
		},
		{
			name: "opposite directions",
			files: map[string]string{
				"001_create_users.up.sql":   "CREATE TABLE users (id INTEGER PRIMARY KEY)",
				"001_create_posts.down.sql": "DROP TABLE posts",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, content := range tt.files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
					t.Fatalf("write migration %s: %v", name, err)
				}
			}

			_, err := LoadSQLMigrations(dir)
			if err == nil {
				t.Fatal("LoadSQLMigrations() error = nil, want duplicate version rejection")
			}
			for _, part := range []string{"001", "create_users", "create_posts"} {
				if !strings.Contains(err.Error(), part) {
					t.Errorf("LoadSQLMigrations() error = %q, want actionable detail %q", err, part)
				}
			}
		})
	}
}

func TestLoadSQLMigrationsAllowsUpAndDownForSameMigration(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"001_create_users.up.sql":   "CREATE TABLE users (id INTEGER PRIMARY KEY)",
		"001_create_users.down.sql": "DROP TABLE users",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatalf("write migration %s: %v", name, err)
		}
	}

	migrations, err := LoadSQLMigrations(dir)
	if err != nil {
		t.Fatalf("LoadSQLMigrations() error = %v", err)
	}
	if len(migrations) != 1 {
		t.Fatalf("LoadSQLMigrations() returned %d migrations, want 1", len(migrations))
	}
	migration := migrations[0]
	if migration.Version != "001" || migration.Name != "create_users" || migration.UpSQL == "" || migration.DownSQL == "" {
		t.Fatalf("LoadSQLMigrations() migration = %#v, want paired 001_create_users migration", migration)
	}
}

package bear

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

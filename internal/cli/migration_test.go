package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func generatedMigrationFixture(t *testing.T) resourceData {
	t.Helper()
	fields, err := parseResourceFields("name:string,amount:decimal,active:bool,published_at:datetime")
	if err != nil {
		t.Fatal(err)
	}
	return resourceData{
		PackageName: "invoice",
		Title:       "Invoice",
		RouteName:   "invoice",
		Fields:      fields,
	}
}

// TestCreateTableSQLMatchesConfiguredDialect keeps the generated migration and
// the runtime dialect in step: the column types must be valid for the database
// the project is configured to use.
func TestCreateTableSQLMatchesConfiguredDialect(t *testing.T) {
	data := generatedMigrationFixture(t)
	tests := []struct {
		dialect  string
		contains []string
	}{
		{
			dialect: "sqlite",
			contains: []string{
				`CREATE TABLE "invoice" (`,
				`"id" INTEGER PRIMARY KEY AUTOINCREMENT`,
				`"name" varchar(255) NOT NULL`,
				`"amount" decimal(10,2) NOT NULL`,
				`"active" tinyint(1),`,
				`"published_at" datetime`,
			},
		},
		{
			dialect: "mysql",
			contains: []string{
				"CREATE TABLE `invoice` (",
				"`id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY",
				"`name` varchar(255) NOT NULL",
				"`active` tinyint(1),",
				"`published_at` datetime",
			},
		},
		{
			dialect: "postgres",
			contains: []string{
				`CREATE TABLE "invoice" (`,
				`"id" BIGSERIAL PRIMARY KEY`,
				`"name" varchar(255) NOT NULL`,
				`"active" BOOLEAN,`,
				`"published_at" TIMESTAMP`,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.dialect, func(t *testing.T) {
			statement, err := createTableSQL(test.dialect, data)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range test.contains {
				if !strings.Contains(statement, want) {
					t.Errorf("generated %s DDL missing %q:\n%s", test.dialect, want, statement)
				}
			}
			for _, forbidden := range []string{"tinyint(1)", "datetime"} {
				if test.dialect == "postgres" && strings.Contains(statement, forbidden) {
					t.Errorf("generated postgres DDL still uses %q:\n%s", forbidden, statement)
				}
			}
		})
	}
}

func TestCreateTableSQLRejectsUnsupportedDialect(t *testing.T) {
	if _, err := createTableSQL("oracle", generatedMigrationFixture(t)); err == nil {
		t.Fatal("createTableSQL accepted an unsupported dialect")
	}
}

// TestNextMigrationVersionContinuesPastExistingFiles keeps a regenerated
// resource from rewriting SQL that may already be applied in an environment.
func TestNextMigrationVersionContinuesPastExistingFiles(t *testing.T) {
	root := t.TempDir()
	version, err := nextMigrationVersion(root)
	if err != nil {
		t.Fatal(err)
	}
	if version != "001" {
		t.Fatalf("first version = %q, want 001", version)
	}

	directory := filepath.Join(root, migrationDirectory)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"001_create_users.up.sql", "001_create_users.down.sql", "007_create_orders.up.sql", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte("SELECT 1;\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	version, err = nextMigrationVersion(root)
	if err != nil {
		t.Fatal(err)
	}
	if version != "008" {
		t.Fatalf("next version = %q, want 008", version)
	}
}

// TestNextMigrationVersionCoversEveryLoadableName is the regression guard for a
// generator that reused a version the framework already occupies. The scan has
// to accept every name shape bear.LoadSQLMigrations accepts, including the
// dashed ones this generator does not write but a hand-authored migration can.
func TestNextMigrationVersionCoversEveryLoadableName(t *testing.T) {
	tests := []struct {
		name  string
		file  string
		want  string
		valid bool
	}{
		{name: "dashed name", file: "008_add-user-email.down.sql", want: "009", valid: true},
		{name: "dashed name up only", file: "012_create-user-profiles.up.sql", want: "013", valid: true},
		{name: "name with dots is rejected by the loader", file: "020_add.user.email.up.sql", valid: false},
		{name: "unknown direction", file: "030_create_users.sideways.sql", valid: false},
		{name: "no separator", file: "040.sql", valid: false},
		{name: "non numeric version", file: "abc_create_users.up.sql", valid: false},
		{name: "not sql", file: "050_create_users.up.txt", valid: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			directory := filepath.Join(root, migrationDirectory)
			if err := os.MkdirAll(directory, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(directory, test.file), []byte("SELECT 1;\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			version, err := nextMigrationVersion(root)
			if err != nil {
				t.Fatal(err)
			}
			want := "001"
			if test.valid {
				want = test.want
			}
			if version != want {
				t.Fatalf("next version = %q, want %q for %s", version, want, test.file)
			}
		})
	}
}

func TestGeneratedAPIAdapterHint(t *testing.T) {
	tests := []struct {
		name     string
		config   string
		contains string
	}{
		{
			name: "project without a configuration is not hinted",
		},
		{
			name:   "enabled database is not hinted",
			config: "database:\n  enabled: true\n  type: \"sqlite\"\n",
		},
		{
			name:     "disabled database is hinted",
			config:   "database:\n  enabled: false\n  type: \"sqlite\"\n",
			contains: "database.enabled",
		},
		{
			name:     "missing database block is hinted",
			config:   "server:\n  port: 8080\n",
			contains: "database.enabled",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if test.config != "" {
				if err := os.WriteFile(filepath.Join(root, scaffoldConfigFile), []byte(test.config), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			hint, err := generatedAPIAdapterHint(root)
			if err != nil {
				t.Fatalf("generatedAPIAdapterHint() error = %v", err)
			}
			if test.contains == "" {
				if hint != "" {
					t.Fatalf("generatedAPIAdapterHint() = %q, want no hint", hint)
				}
				return
			}
			if !strings.Contains(hint, test.contains) {
				t.Fatalf("hint %q does not contain %q", hint, test.contains)
			}
		})
	}
}

// A disabled database must not stop `bear gen api`. The release E2E generates a
// resource into a project whose configuration disables the database and then
// registers its own *bear.GormAdapter, so refusing the command would reject a
// runnable project. Pin the non-blocking behaviour and the hint that replaces
// the old refusal.
func TestGeneratedAPIWithDisabledDatabaseIsWarnedNotRefused(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, scaffoldConfigFile), []byte("database:\n  enabled: false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hint, err := generatedAPIAdapterHint(root)
	if err != nil {
		t.Fatalf("generatedAPIAdapterHint() error = %v", err)
	}
	if hint == "" {
		t.Fatal("generatedAPIAdapterHint() = empty, want a hint about the disabled database")
	}
	for _, want := range []string{"database.enabled", "*bear.GormAdapter", "BeansE"} {
		if !strings.Contains(hint, want) {
			t.Fatalf("hint %q does not mention %q", hint, want)
		}
	}
}

// The command must still exit 0 and keep stdout machine-readable; only stderr
// carries the hint.
func TestExecuteGenAPIWarnsWhenDatabaseIsDisabled(t *testing.T) {
	project := t.TempDir()
	t.Chdir(project)
	if err := os.WriteFile(filepath.Join(project, "go.mod"), []byte("module example.com/invoice\n\ngo 1.25.14\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, scaffoldConfigFile), []byte("database:\n  enabled: false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Execute([]string{"gen", "api", "invoice", "--fields", "amount:decimal"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("gen exit code = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "Warning:") || !strings.Contains(stderr.String(), "database.enabled") {
		t.Fatalf("stderr = %q, want a warning naming database.enabled", stderr.String())
	}
	if !strings.Contains(stdout.String(), "Generated internal/invoice") {
		t.Fatalf("stdout = %q, want the generated path", stdout.String())
	}
	if strings.Contains(stdout.String(), "Warning:") {
		t.Fatalf("stdout = %q, want warnings kept off stdout", stdout.String())
	}
}

func TestWriteGeneratedAPIMigrationPublishesBothDirections(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, scaffoldConfigFile), []byte("database:\n  enabled: true\n  type: \"sqlite\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	written, err := writeGeneratedAPIMigration(root, generatedMigrationFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"migrations/001_create_invoice.up.sql", "migrations/001_create_invoice.down.sql"}
	if len(written) != len(want) {
		t.Fatalf("written = %v, want %v", written, want)
	}
	for i, path := range want {
		if written[i] != path {
			t.Fatalf("written[%d] = %q, want %q", i, written[i], path)
		}
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(path))); err != nil {
			t.Fatalf("migration %s was not published: %v", path, err)
		}
	}
	if contents := readGeneratedFile(t, filepath.Join(root, migrationDirectory), "001_create_invoice.down.sql"); !strings.Contains(contents, `DROP TABLE IF EXISTS "invoice"`) {
		t.Fatalf("down migration does not drop the table:\n%s", contents)
	}
}

func TestWriteGeneratedAPIMigrationSkipsDisabledDatabase(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, scaffoldConfigFile), []byte("database:\n  enabled: false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	written, err := writeGeneratedAPIMigration(root, generatedMigrationFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(written) != 0 {
		t.Fatalf("written = %v, want none while the database is disabled", written)
	}
	if _, err := os.Stat(filepath.Join(root, migrationDirectory)); !os.IsNotExist(err) {
		t.Fatalf("a disabled database still produced a migrations directory: %v", err)
	}
}

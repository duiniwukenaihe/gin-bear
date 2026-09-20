package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/duiniwukenaihe/gin-bear/pkg/bear"
)

// migrationDirectory matches the reviewed-file layout documented in
// docs/production.md. The framework never applies schema changes from
// request-serving code, so `bear gen api` writes the SQL and the operator runs
// it as a separate deploy step.
const migrationDirectory = "migrations"

// generatedAPIDatabase is the database contract a generated API resource needs.
type generatedAPIDatabase struct {
	// Known reports whether the project has a configuration to inspect. Projects
	// without application.yaml keep the generator's previous behaviour.
	Known bool
	// Enabled mirrors database.enabled. Every generated repository injects
	// *bear.GormAdapter, which only exists while the database is enabled.
	Enabled bool
	// Dialect is the migration dialect implied by database.type.
	Dialect string
}

// inspectGeneratedAPIDatabase reads the project configuration so the generator
// can refuse to emit a resource the project cannot run.
func inspectGeneratedAPIDatabase(root string) (generatedAPIDatabase, error) {
	return resolveGeneratedAPIDatabase(root, nil)
}

// resolveGeneratedAPIDatabase selects the database contract once per
// generation using the shared bear parsing rules. Explicit paths replace the
// default file chain; relative paths resolve against the project root.
func resolveGeneratedAPIDatabase(root string, configPaths []string) (generatedAPIDatabase, error) {
	snapshot, err := bear.LoadDatabaseConfigForGeneration(root, configPaths...)
	if err != nil {
		return generatedAPIDatabase{}, fmt.Errorf("read generation database config: %w", err)
	}
	if snapshot == nil {
		return generatedAPIDatabase{}, nil
	}
	database := generatedAPIDatabase{Known: true, Enabled: snapshot.Enabled, Dialect: migrationDialect(snapshot.Type)}
	if database.Dialect == "" {
		database.Dialect = migrationDialect("")
	}
	return database, nil
}

// generatedAPIAdapterHint explains how to satisfy the *bear.GormAdapter that
// every generated repository injects. It returns "" when the project already
// enables a database, or when there is no configuration to inspect.
//
// A disabled database is a hint rather than a refusal. The framework only
// registers *bear.GormAdapter while database.enabled is true, but the bean can
// also come from the application itself: the release E2E opens an in-memory
// SQLite handle and calls BeansE(&bear.GormAdapter{DB: db}), and any project
// that owns its *gorm.DB can do the same. Refusing the command would reject a
// configuration that runs, so the mismatch is reported without blocking.
func generatedAPIAdapterHint(root string) (string, error) {
	database, err := inspectGeneratedAPIDatabase(root)
	if err != nil {
		return "", err
	}
	return adapterHintForDatabase(root, database), nil
}

// adapterHintForDatabase derives the disabled-database hint from an already
// resolved snapshot so warnings share the single generation parse and never
// re-read configuration files.
func adapterHintForDatabase(root string, database generatedAPIDatabase) string {
	if !database.Known || database.Enabled {
		return ""
	}
	return fmt.Sprintf(`%s sets database.enabled to false, so the framework registers no *bear.GormAdapter.
Every generated repository injects one, so the generated resource only starts once an
adapter is available. Either enable a database:

    database:
      enabled: true
      type: "sqlite"
      dsn: "%s.db"

or register *bear.GormAdapter yourself before serving, for example
application.BeansE(&bear.GormAdapter{DB: db}). Use "mysql" or "postgres" together
with host, user, password and dbname for a real deployment, then run migrations
before starting the server`,
		scaffoldConfigFile, filepath.Base(root))
}

// migrationDialect normalises database.type onto the dialect names the generated
// SQL targets. An empty type follows the runtime default, which is MySQL.
func migrationDialect(dbType string) string {
	switch strings.ToLower(strings.TrimSpace(dbType)) {
	case "postgres", "postgresql":
		return "postgres"
	case "mysql", "":
		return "mysql"
	case "sqlite", "sqlite3":
		return "sqlite"
	default:
		return strings.ToLower(strings.TrimSpace(dbType))
	}
}

// resourceMigration is the reviewed SQL a generated API resource needs before it
// can serve traffic.
type resourceMigration struct {
	Version string
	Name    string
	Table   string
	UpSQL   string
	DownSQL string
}

// resourceMigrationFor renders the create-table pair for one generated resource.
func resourceMigrationFor(root string, data resourceData, dialect string) (resourceMigration, error) {
	version, err := nextMigrationVersion(root)
	if err != nil {
		return resourceMigration{}, err
	}
	up, err := createTableSQL(dialect, data)
	if err != nil {
		return resourceMigration{}, err
	}
	return resourceMigration{
		Version: version,
		Name:    "create_" + strings.ReplaceAll(data.RouteName, "-", "_"),
		Table:   data.RouteName,
		UpSQL:   up,
		DownSQL: fmt.Sprintf("DROP TABLE IF EXISTS %s;\n", quoteIdentifier(dialect, data.RouteName)),
	}, nil
}

// writeGeneratedAPIMigration writes the reviewed migration pair for a resource
// when the project configuration names an enabled database. Projects that keep
// the database disabled get no migration, matching their previous behaviour.
func writeGeneratedAPIMigration(root string, data resourceData) ([]string, error) {
	database, err := inspectGeneratedAPIDatabase(root)
	if err != nil {
		return nil, err
	}
	return writeGeneratedAPIMigrationWithDatabase(root, data, database)
}

// writeGeneratedAPIMigrationWithDatabase writes the reviewed migration pair
// from an already resolved snapshot so dialect selection shares the single
// generation parse instead of re-reading configuration files.
func writeGeneratedAPIMigrationWithDatabase(root string, data resourceData, database generatedAPIDatabase) ([]string, error) {
	if !database.Enabled {
		return nil, nil
	}
	migration, err := resourceMigrationFor(root, data, database.Dialect)
	if err != nil {
		return nil, fmt.Errorf("plan resource migration: %w", err)
	}
	files, err := writeResourceMigration(root, migration)
	if err != nil {
		return files, fmt.Errorf("write resource migration: %w", err)
	}
	return files, nil
}

// nextMigrationVersion returns the next zero-padded version, one past the highest
// version already present so an applied migration is never rewritten.
//
// The scan mirrors bear.LoadSQLMigrations: a migration file is
// "<version>_<name>.<up|down>.sql" with the version ahead of the first
// underscore. Accepting exactly what the framework loads is what keeps the
// generator from reusing a version that already exists under a name shape this
// file happens not to like.
func nextMigrationVersion(root string) (string, error) {
	entries, err := os.ReadDir(filepath.Join(root, migrationDirectory))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "001", nil
		}
		return "", fmt.Errorf("read %s: %w", migrationDirectory, err)
	}
	highest := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		version, ok := migrationVersionOf(entry.Name())
		if !ok {
			continue
		}
		if version > highest {
			highest = version
		}
	}
	return fmt.Sprintf("%03d", highest+1), nil
}

// migrationVersionOf reports the numeric version of a migration file name,
// following the framework's own parsing rules so the generator and the runner
// agree on which files occupy the version space.
func migrationVersionOf(name string) (int, bool) {
	base, ok := strings.CutSuffix(name, ".sql")
	if !ok {
		return 0, false
	}
	parts := strings.Split(base, ".")
	if len(parts) != 2 || (parts[1] != "up" && parts[1] != "down") {
		return 0, false
	}
	version, migrationName, found := strings.Cut(parts[0], "_")
	if !found || version == "" || migrationName == "" {
		return 0, false
	}
	parsed, err := strconv.Atoi(version)
	if err != nil || parsed < 0 {
		return 0, false
	}
	return parsed, true
}

// createTableSQL renders CREATE TABLE for the columns the generated model reads
// and writes. Column names follow GORM's snake_case convention so the SQL and the
// model agree without extra mapping.
func createTableSQL(dialect string, data resourceData) (string, error) {
	primaryKey, err := primaryKeySQL(dialect)
	if err != nil {
		return "", err
	}
	columns := make([]string, 0, len(data.Fields)+1)
	columns = append(columns, "    "+primaryKey)
	for _, item := range data.Fields {
		columnType, err := columnSQLType(dialect, item)
		if err != nil {
			return "", err
		}
		column := fmt.Sprintf("    %s %s", quoteIdentifier(dialect, item.JSONName), columnType)
		if strings.Contains(item.Validate, "required") {
			column += " NOT NULL"
		}
		columns = append(columns, column)
	}
	return fmt.Sprintf("CREATE TABLE %s (\n%s\n);\n", quoteIdentifier(dialect, data.RouteName), strings.Join(columns, ",\n")), nil
}

func primaryKeySQL(dialect string) (string, error) {
	id := quoteIdentifier(dialect, "id")
	switch migrationDialect(dialect) {
	case "sqlite":
		return id + " INTEGER PRIMARY KEY AUTOINCREMENT", nil
	case "mysql":
		return id + " BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY", nil
	case "postgres":
		return id + " BIGSERIAL PRIMARY KEY", nil
	default:
		return "", fmt.Errorf("unsupported database type %q: cannot generate migrations", dialect)
	}
}

// columnSQLType maps a generated field onto a column type. The GORM tag already
// carries the intended SQL type, so only the types SQLite and MySQL accept but
// PostgreSQL rejects need translating.
func columnSQLType(dialect string, item field) (string, error) {
	spec, _, _ := strings.Cut(item.GormTag, ";")
	base := strings.TrimPrefix(spec, "type:")
	if base == spec || base == "" {
		return "", fmt.Errorf("generated field %q has no column type in its GORM tag %q", item.Name, item.GormTag)
	}
	if migrationDialect(dialect) != "postgres" {
		return base, nil
	}
	switch base {
	case "tinyint(1)":
		if item.GoType == "bool" {
			return "BOOLEAN", nil
		}
		return "SMALLINT", nil
	case "datetime":
		return "TIMESTAMP", nil
	case "float":
		return "REAL", nil
	default:
		return base, nil
	}
}

func quoteIdentifier(dialect, name string) string {
	if migrationDialect(dialect) == "mysql" {
		return "`" + name + "`"
	}
	return `"` + name + `"`
}

// writeResourceMigration writes the reviewed up/down pair. Each file lands
// atomically so an interrupted run cannot leave a truncated migration that
// bear.LoadSQLMigrations would pick up.
func writeResourceMigration(root string, migration resourceMigration) ([]string, error) {
	directions := []struct {
		suffix   string
		contents string
	}{
		{suffix: "up", contents: migration.UpSQL},
		{suffix: "down", contents: migration.DownSQL},
	}
	written := make([]string, 0, len(directions))
	for _, direction := range directions {
		relative := filepath.Join(migrationDirectory, fmt.Sprintf("%s_%s.%s.sql", migration.Version, migration.Name, direction.suffix))
		path := filepath.Join(root, relative)
		if _, err := os.Lstat(path); err == nil {
			return written, fmt.Errorf("migration %q already exists", filepath.ToSlash(relative))
		} else if !errors.Is(err, os.ErrNotExist) {
			return written, fmt.Errorf("inspect migration %q: %w", filepath.ToSlash(relative), err)
		}
		if err := writeGeneratedFileAtomic(path, []byte(direction.contents), 0644); err != nil {
			return written, err
		}
		written = append(written, filepath.ToSlash(relative))
	}
	return written, nil
}

// removeGeneratedFiles rolls back migrations written for a resource that failed
// to publish.
func removeGeneratedFiles(root string, relative []string) {
	for _, path := range relative {
		_ = os.Remove(filepath.Join(root, filepath.FromSlash(path)))
	}
}

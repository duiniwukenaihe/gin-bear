package bear

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const defaultMigrationTable = "schema_migrations"

type Migration struct {
	Version string
	Name    string
	UpSQL   string
	DownSQL string
}

type MigrationRunner struct {
	DB    *sql.DB
	Table string
}

func NewMigrationRunner(db *sql.DB) *MigrationRunner {
	return &MigrationRunner{
		DB:    db,
		Table: defaultMigrationTable,
	}
}

func LoadSQLMigrations(dir string) ([]Migration, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	byKey := make(map[string]*Migration)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		version, migrationName, direction, ok := parseMigrationFileName(name)
		if !ok {
			continue
		}
		path := filepath.Join(dir, name)
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		key := version + "_" + migrationName
		migration := byKey[key]
		if migration == nil {
			migration = &Migration{Version: version, Name: migrationName}
			byKey[key] = migration
		}
		switch direction {
		case "up":
			migration.UpSQL = string(content)
		case "down":
			migration.DownSQL = string(content)
		}
	}

	migrations := make([]Migration, 0, len(byKey))
	for _, migration := range byKey {
		if strings.TrimSpace(migration.UpSQL) == "" {
			continue
		}
		migrations = append(migrations, *migration)
	}
	sort.Slice(migrations, func(i, j int) bool {
		if migrations[i].Version != migrations[j].Version {
			return migrations[i].Version < migrations[j].Version
		}
		return migrations[i].Name < migrations[j].Name
	})
	return migrations, nil
}

func parseMigrationFileName(name string) (version, migrationName, direction string, ok bool) {
	if !strings.HasSuffix(name, ".sql") {
		return "", "", "", false
	}
	base := strings.TrimSuffix(name, ".sql")
	parts := strings.Split(base, ".")
	if len(parts) != 2 {
		return "", "", "", false
	}
	if parts[1] != "up" && parts[1] != "down" {
		return "", "", "", false
	}
	version, migrationName, found := strings.Cut(parts[0], "_")
	if !found || version == "" || migrationName == "" {
		return "", "", "", false
	}
	return version, migrationName, parts[1], true
}

func (r *MigrationRunner) Up(ctx context.Context, migrations []Migration) error {
	if r == nil || r.DB == nil {
		return fmt.Errorf("migration runner requires a database")
	}
	table := r.Table
	if table == "" {
		table = defaultMigrationTable
	}
	if err := r.ensureTable(ctx, table); err != nil {
		return err
	}

	for _, migration := range migrations {
		applied, err := r.isApplied(ctx, table, migration.Version)
		if err != nil {
			return err
		}
		if applied {
			continue
		}
		if err := r.applyUp(ctx, table, migration); err != nil {
			return err
		}
	}
	return nil
}

func (r *MigrationRunner) ensureTable(ctx context.Context, table string) error {
	query := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
version TEXT PRIMARY KEY,
name TEXT NOT NULL,
applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
)`, table)
	_, err := r.DB.ExecContext(ctx, query)
	return err
}

func (r *MigrationRunner) isApplied(ctx context.Context, table string, version string) (bool, error) {
	var existing string
	query := fmt.Sprintf("SELECT version FROM %s WHERE version = ?", table)
	err := r.DB.QueryRowContext(ctx, query, version).Scan(&existing)
	if err == nil {
		return true, nil
	}
	if err == sql.ErrNoRows {
		return false, nil
	}
	return false, err
}

func (r *MigrationRunner) applyUp(ctx context.Context, table string, migration Migration) error {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if _, err := tx.ExecContext(ctx, migration.UpSQL); err != nil {
		return fmt.Errorf("apply migration %s_%s: %w", migration.Version, migration.Name, err)
	}
	insert := fmt.Sprintf("INSERT INTO %s (version, name) VALUES (?, ?)", table)
	if _, err := tx.ExecContext(ctx, insert, migration.Version, migration.Name); err != nil {
		return fmt.Errorf("record migration %s_%s: %w", migration.Version, migration.Name, err)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

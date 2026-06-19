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
const defaultMigrationLockTable = "schema_migration_locks"
const defaultMigrationLockName = "global"

type Migration struct {
	Version string
	Name    string
	UpSQL   string
	DownSQL string
}

type MigrationRunner struct {
	DB        *sql.DB
	Table     string
	LockTable string
}

func NewMigrationRunner(db *sql.DB) *MigrationRunner {
	return &MigrationRunner{
		DB:        db,
		Table:     defaultMigrationTable,
		LockTable: defaultMigrationLockTable,
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
	if err := validateMigrationTableName(table); err != nil {
		return err
	}
	if err := r.ensureTable(ctx, table); err != nil {
		return err
	}
	lockTable := r.lockTable()
	if err := validateMigrationTableName(lockTable); err != nil {
		return err
	}
	if err := r.ensureLockTable(ctx, lockTable); err != nil {
		return err
	}
	if err := r.acquireLock(ctx, lockTable); err != nil {
		return err
	}
	defer r.releaseLock(context.Background(), lockTable)

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

func (r *MigrationRunner) Down(ctx context.Context, migrations []Migration, steps int) error {
	if r == nil || r.DB == nil {
		return fmt.Errorf("migration runner requires a database")
	}
	if steps <= 0 {
		return nil
	}
	table := r.Table
	if table == "" {
		table = defaultMigrationTable
	}
	if err := validateMigrationTableName(table); err != nil {
		return err
	}
	if err := r.ensureTable(ctx, table); err != nil {
		return err
	}
	lockTable := r.lockTable()
	if err := validateMigrationTableName(lockTable); err != nil {
		return err
	}
	if err := r.ensureLockTable(ctx, lockTable); err != nil {
		return err
	}
	if err := r.acquireLock(ctx, lockTable); err != nil {
		return err
	}
	defer r.releaseLock(context.Background(), lockTable)

	latest, err := r.latestApplied(ctx, table, steps)
	if err != nil {
		return err
	}
	byVersion := make(map[string]Migration, len(migrations))
	for _, migration := range migrations {
		byVersion[migration.Version] = migration
	}
	for _, applied := range latest {
		migration, ok := byVersion[applied.Version]
		if !ok {
			return fmt.Errorf("rollback migration %s_%s: migration file not loaded", applied.Version, applied.Name)
		}
		if strings.TrimSpace(migration.DownSQL) == "" {
			return fmt.Errorf("rollback migration %s_%s: down sql is empty", applied.Version, applied.Name)
		}
		if err := r.applyDown(ctx, table, migration); err != nil {
			return err
		}
	}
	return nil
}

func (r *MigrationRunner) ForceUnlock(ctx context.Context) error {
	if r == nil || r.DB == nil {
		return fmt.Errorf("migration runner requires a database")
	}
	lockTable := r.lockTable()
	if err := validateMigrationTableName(lockTable); err != nil {
		return err
	}
	if err := r.ensureLockTable(ctx, lockTable); err != nil {
		return err
	}
	r.releaseLock(ctx, lockTable)
	return nil
}

func validateMigrationTableName(name string) error {
	if name == "" {
		return fmt.Errorf("invalid migration table name %q", name)
	}
	for i, r := range name {
		if r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (i > 0 && r >= '0' && r <= '9') {
			continue
		}
		return fmt.Errorf("invalid migration table name %q", name)
	}
	if name[0] >= '0' && name[0] <= '9' {
		return fmt.Errorf("invalid migration table name %q", name)
	}
	return nil
}

func (r *MigrationRunner) lockTable() string {
	if r.LockTable != "" {
		return r.LockTable
	}
	return defaultMigrationLockTable
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

func (r *MigrationRunner) ensureLockTable(ctx context.Context, table string) error {
	query := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
name TEXT PRIMARY KEY,
locked_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
)`, table)
	_, err := r.DB.ExecContext(ctx, query)
	return err
}

func (r *MigrationRunner) acquireLock(ctx context.Context, table string) error {
	query := fmt.Sprintf("INSERT INTO %s (name) VALUES (?)", table)
	if _, err := r.DB.ExecContext(ctx, query, defaultMigrationLockName); err != nil {
		return fmt.Errorf("migration lock is already held: %w", err)
	}
	return nil
}

func (r *MigrationRunner) releaseLock(ctx context.Context, table string) {
	query := fmt.Sprintf("DELETE FROM %s WHERE name = ?", table)
	_, _ = r.DB.ExecContext(ctx, query, defaultMigrationLockName)
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

type appliedMigration struct {
	Version string
	Name    string
}

func (r *MigrationRunner) latestApplied(ctx context.Context, table string, steps int) ([]appliedMigration, error) {
	query := fmt.Sprintf("SELECT version, name FROM %s ORDER BY version DESC LIMIT ?", table)
	rows, err := r.DB.QueryContext(ctx, query, steps)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var applied []appliedMigration
	for rows.Next() {
		var migration appliedMigration
		if err := rows.Scan(&migration.Version, &migration.Name); err != nil {
			return nil, err
		}
		applied = append(applied, migration)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return applied, nil
}

func (r *MigrationRunner) applyDown(ctx context.Context, table string, migration Migration) error {
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

	if _, err := tx.ExecContext(ctx, migration.DownSQL); err != nil {
		return fmt.Errorf("rollback migration %s_%s: %w", migration.Version, migration.Name, err)
	}
	deleteRecord := fmt.Sprintf("DELETE FROM %s WHERE version = ?", table)
	if _, err := tx.ExecContext(ctx, deleteRecord, migration.Version); err != nil {
		return fmt.Errorf("remove migration record %s_%s: %w", migration.Version, migration.Name, err)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

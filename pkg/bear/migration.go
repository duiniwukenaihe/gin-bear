package bear

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
)

const defaultMigrationTable = "schema_migrations"
const defaultMigrationLockTable = "schema_migration_locks"
const defaultMigrationLockName = "global"
const defaultMigrationReleaseTimeout = 5 * time.Second
const legacyMigrationLockOwner = "legacy-unowned"

var ErrMigrationLockOwnerMismatch = errors.New("migration lock owner mismatch")
var ErrDirtyMigration = errors.New("dirty migration state")

// MigrationDialect controls placeholder rebinding and dialect-specific DDL.
type MigrationDialect string

const (
	MigrationDialectSQLite     MigrationDialect = "sqlite"
	MigrationDialectMySQL      MigrationDialect = "mysql"
	MigrationDialectPostgreSQL MigrationDialect = "postgresql"
)

// Rebind converts framework-generated question-mark placeholders for the
// configured database. Migration SQL itself is always executed verbatim.
func (d MigrationDialect) Rebind(query string) (string, error) {
	switch d {
	case MigrationDialectSQLite, MigrationDialectMySQL:
		return query, nil
	case MigrationDialectPostgreSQL:
		var rebound strings.Builder
		rebound.Grow(len(query) + 8)
		parameter := 1
		for _, char := range query {
			if char != '?' {
				rebound.WriteRune(char)
				continue
			}
			rebound.WriteByte('$')
			rebound.WriteString(strconv.Itoa(parameter))
			parameter++
		}
		return rebound.String(), nil
	default:
		return "", fmt.Errorf("unsupported migration dialect %q", d)
	}
}

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

// DialectMigrationRunner executes migrations with one immutable explicit
// dialect while preserving access to MigrationRunner's table configuration.
type DialectMigrationRunner struct {
	runner  *MigrationRunner
	dialect MigrationDialect
}

func NewMigrationRunner(db *sql.DB) *MigrationRunner {
	return &MigrationRunner{
		DB:        db,
		Table:     defaultMigrationTable,
		LockTable: defaultMigrationLockTable,
	}
}

// NewMigrationRunnerWithDialect creates a runner with an explicit SQL
// dialect without changing MigrationRunner's source-compatible public shape.
func NewMigrationRunnerWithDialect(db *sql.DB, dialect MigrationDialect) *DialectMigrationRunner {
	return &DialectMigrationRunner{
		runner:  NewMigrationRunner(db),
		dialect: dialect,
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

func (r *MigrationRunner) Up(ctx context.Context, migrations []Migration) (resultErr error) {
	dialect, err := r.inferDialect()
	if err != nil {
		return err
	}
	return r.up(ctx, migrations, dialect)
}

// Up applies migrations using the explicit runner dialect.
func (r *DialectMigrationRunner) Up(ctx context.Context, migrations []Migration) error {
	if r == nil || r.runner == nil {
		return fmt.Errorf("migration runner requires a database")
	}
	return r.runner.up(ctx, migrations, r.dialect)
}

func (r *MigrationRunner) up(ctx context.Context, migrations []Migration, dialect MigrationDialect) (resultErr error) {
	if r == nil || r.DB == nil {
		return fmt.Errorf("migration runner requires a database")
	}
	if err := validateMigrationDialect(dialect); err != nil {
		return err
	}
	table := r.Table
	if table == "" {
		table = defaultMigrationTable
	}
	if err := validateMigrationTableName(table); err != nil {
		return err
	}
	if err := r.ensureTable(ctx, table, dialect); err != nil {
		return err
	}
	lockTable := r.lockTable()
	if err := validateMigrationTableName(lockTable); err != nil {
		return err
	}
	if err := r.ensureLockTable(ctx, lockTable, dialect); err != nil {
		return err
	}
	owner, err := r.acquireLock(ctx, lockTable, dialect)
	if err != nil {
		return err
	}
	defer func() {
		if err := r.releaseLockBounded(lockTable, owner, dialect); resultErr == nil && err != nil {
			resultErr = err
		}
	}()
	if err := r.rejectDirtyMigration(ctx, table); err != nil {
		return err
	}

	for _, migration := range migrations {
		applied, err := r.isApplied(ctx, table, migration.Version, dialect)
		if err != nil {
			return err
		}
		if applied {
			continue
		}
		if err := r.applyUp(ctx, table, migration, dialect); err != nil {
			return err
		}
	}
	return nil
}

func (r *MigrationRunner) Down(ctx context.Context, migrations []Migration, steps int) (resultErr error) {
	dialect, err := r.inferDialect()
	if err != nil {
		return err
	}
	return r.down(ctx, migrations, steps, dialect)
}

// Down rolls back migrations using the explicit runner dialect.
func (r *DialectMigrationRunner) Down(ctx context.Context, migrations []Migration, steps int) error {
	if r == nil || r.runner == nil {
		return fmt.Errorf("migration runner requires a database")
	}
	return r.runner.down(ctx, migrations, steps, r.dialect)
}

func (r *MigrationRunner) down(ctx context.Context, migrations []Migration, steps int, dialect MigrationDialect) (resultErr error) {
	if r == nil || r.DB == nil {
		return fmt.Errorf("migration runner requires a database")
	}
	if err := validateMigrationDialect(dialect); err != nil {
		return err
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
	if err := r.ensureTable(ctx, table, dialect); err != nil {
		return err
	}
	lockTable := r.lockTable()
	if err := validateMigrationTableName(lockTable); err != nil {
		return err
	}
	if err := r.ensureLockTable(ctx, lockTable, dialect); err != nil {
		return err
	}
	owner, err := r.acquireLock(ctx, lockTable, dialect)
	if err != nil {
		return err
	}
	defer func() {
		if err := r.releaseLockBounded(lockTable, owner, dialect); resultErr == nil && err != nil {
			resultErr = err
		}
	}()
	if err := r.rejectDirtyMigration(ctx, table); err != nil {
		return err
	}

	latest, err := r.latestApplied(ctx, table, steps, dialect)
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
		if err := r.applyDown(ctx, table, migration, dialect); err != nil {
			return err
		}
	}
	return nil
}

// ForceUnlock is an unconditional operator recovery action. It snapshots the
// current owner and deletes only that owner, so a concurrently replaced lock
// is preserved. Callers decide whether a lock is stale before invoking it.
func (r *MigrationRunner) ForceUnlock(ctx context.Context) error {
	dialect, err := r.inferDialect()
	if err != nil {
		return err
	}
	return r.forceUnlock(ctx, dialect)
}

// ForceUnlock removes the snapshotted lock owner using the explicit dialect.
func (r *DialectMigrationRunner) ForceUnlock(ctx context.Context) error {
	if r == nil || r.runner == nil {
		return fmt.Errorf("migration runner requires a database")
	}
	return r.runner.forceUnlock(ctx, r.dialect)
}

func (r *MigrationRunner) forceUnlock(ctx context.Context, dialect MigrationDialect) error {
	if r == nil || r.DB == nil {
		return fmt.Errorf("migration runner requires a database")
	}
	if err := validateMigrationDialect(dialect); err != nil {
		return err
	}
	lockTable := r.lockTable()
	if err := validateMigrationTableName(lockTable); err != nil {
		return err
	}
	if err := r.ensureLockTable(ctx, lockTable, dialect); err != nil {
		return err
	}

	query, err := dialect.Rebind(fmt.Sprintf("SELECT owner FROM %s WHERE name = ?", lockTable))
	if err != nil {
		return err
	}
	var owner string
	if err := r.DB.QueryRowContext(ctx, query, defaultMigrationLockName).Scan(&owner); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("read migration lock owner: %w", err)
	}
	if err := r.releaseLock(ctx, lockTable, owner, dialect); err != nil {
		return fmt.Errorf("force unlock migration lock: %w", err)
	}
	return nil
}

// ForceMigrationState resolves one dirty migration after an operator has
// inspected the database. applied=true keeps the history row and marks the
// migration complete; applied=false removes it so the migration can run again.
// It never executes migration SQL or infers which state the schema reached.
func (r *DialectMigrationRunner) ForceMigrationState(ctx context.Context, version string, applied bool) error {
	if r == nil || r.runner == nil {
		return fmt.Errorf("migration runner requires a database")
	}
	return r.runner.forceMigrationState(ctx, version, applied, r.dialect)
}

func (r *MigrationRunner) forceMigrationState(ctx context.Context, version string, applied bool, dialect MigrationDialect) (resultErr error) {
	if r == nil || r.DB == nil {
		return fmt.Errorf("migration runner requires a database")
	}
	if strings.TrimSpace(version) == "" {
		return fmt.Errorf("force migration state requires a version")
	}
	if err := validateMigrationDialect(dialect); err != nil {
		return err
	}
	table := r.Table
	if table == "" {
		table = defaultMigrationTable
	}
	if err := validateMigrationTableName(table); err != nil {
		return err
	}
	if err := r.ensureTable(ctx, table, dialect); err != nil {
		return err
	}
	lockTable := r.lockTable()
	if err := validateMigrationTableName(lockTable); err != nil {
		return err
	}
	if err := r.ensureLockTable(ctx, lockTable, dialect); err != nil {
		return err
	}
	owner, err := r.acquireLock(ctx, lockTable, dialect)
	if err != nil {
		return err
	}
	defer func() {
		if err := r.releaseLockBounded(lockTable, owner, dialect); resultErr == nil && err != nil {
			resultErr = err
		}
	}()

	statement := fmt.Sprintf("DELETE FROM %s WHERE version = ? AND dirty = TRUE", table)
	if applied {
		statement = fmt.Sprintf("UPDATE %s SET dirty = FALSE, applied_at = CURRENT_TIMESTAMP WHERE version = ? AND dirty = TRUE", table)
	}
	statement, err = dialect.Rebind(statement)
	if err != nil {
		return err
	}
	result, err := r.DB.ExecContext(ctx, statement, version)
	if err != nil {
		return fmt.Errorf("force migration %s state: %w", version, err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect forced migration %s state: %w", version, err)
	}
	if changed != 1 {
		return fmt.Errorf("force migration %s state: dirty migration not found", version)
	}
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

func validateMigrationDialect(dialect MigrationDialect) error {
	_, err := dialect.Rebind("")
	return err
}

func (r *MigrationRunner) rebind(query string) (string, error) {
	dialect, err := r.inferDialect()
	if err != nil {
		return "", err
	}
	return dialect.Rebind(query)
}

func (r *DialectMigrationRunner) rebind(query string) (string, error) {
	if r == nil {
		return "", fmt.Errorf("migration runner requires a database")
	}
	return r.dialect.Rebind(query)
}

func (r *MigrationRunner) inferDialect() (MigrationDialect, error) {
	if r == nil || r.DB == nil {
		return "", fmt.Errorf("migration runner requires a database")
	}
	return inferMigrationDialect(r.DB)
}

func inferMigrationDialect(db *sql.DB) (MigrationDialect, error) {
	driverType := reflect.TypeOf(db.Driver())
	if driverType.Kind() == reflect.Ptr {
		driverType = driverType.Elem()
	}
	identity := strings.ToLower(driverType.PkgPath() + " " + driverType.String())
	switch {
	case strings.Contains(identity, "mysql"):
		return MigrationDialectMySQL, nil
	case strings.Contains(identity, "postgres"), strings.Contains(identity, "pgx"), strings.Contains(identity, "lib/pq"):
		return MigrationDialectPostgreSQL, nil
	case strings.Contains(identity, "sqlite"):
		return MigrationDialectSQLite, nil
	default:
		return "", fmt.Errorf("cannot infer migration dialect from driver %T; use NewMigrationRunnerWithDialect", db.Driver())
	}
}

func (r *MigrationRunner) ensureTable(ctx context.Context, table string, dialect MigrationDialect) error {
	textType := "TEXT"
	if dialect == MigrationDialectMySQL {
		textType = "VARCHAR(255)"
	}
	query := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
version %s PRIMARY KEY,
name %s NOT NULL,
applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
dirty BOOLEAN NOT NULL DEFAULT FALSE
)`, table, textType, textType)
	if _, err := r.DB.ExecContext(ctx, query); err != nil {
		return err
	}
	if r.migrationTableHasDirty(ctx, table) {
		return nil
	}
	alter := fmt.Sprintf("ALTER TABLE %s ADD COLUMN dirty BOOLEAN NOT NULL DEFAULT FALSE", table)
	_, alterErr := r.DB.ExecContext(ctx, alter)
	if r.migrationTableHasDirty(ctx, table) {
		return nil
	}
	if alterErr != nil {
		return fmt.Errorf("add migration dirty column: %w", alterErr)
	}
	return fmt.Errorf("add migration dirty column: dirty column is unavailable")
}

func (r *MigrationRunner) migrationTableHasDirty(ctx context.Context, table string) bool {
	rows, err := r.DB.QueryContext(ctx, fmt.Sprintf("SELECT dirty FROM %s WHERE 1 = 0", table))
	if err != nil {
		return false
	}
	_ = rows.Close()
	return true
}

func (r *MigrationRunner) ensureLockTable(ctx context.Context, table string, dialect MigrationDialect) error {
	textType := "TEXT"
	if dialect == MigrationDialectMySQL {
		textType = "VARCHAR(255)"
	}
	query := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
name %s PRIMARY KEY,
owner %s NOT NULL,
locked_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
)`, table, textType, textType)
	if _, err := r.DB.ExecContext(ctx, query); err != nil {
		return err
	}
	if r.lockTableHasOwner(ctx, table) {
		return nil
	}
	alter := fmt.Sprintf("ALTER TABLE %s ADD COLUMN owner %s NOT NULL DEFAULT '%s'", table, textType, legacyMigrationLockOwner)
	_, alterErr := r.DB.ExecContext(ctx, alter)
	if r.lockTableHasOwner(ctx, table) {
		return nil
	}
	if alterErr != nil {
		return fmt.Errorf("add migration lock owner column: %w", alterErr)
	}
	return fmt.Errorf("add migration lock owner column: owner column is unavailable")
}

func (r *MigrationRunner) lockTableHasOwner(ctx context.Context, table string) bool {
	rows, err := r.DB.QueryContext(ctx, fmt.Sprintf("SELECT owner FROM %s WHERE 1 = 0", table))
	if err != nil {
		return false
	}
	_ = rows.Close()
	return true
}

func (r *MigrationRunner) acquireLock(ctx context.Context, table string, dialect MigrationDialect) (string, error) {
	owner, err := newMigrationLockOwner()
	if err != nil {
		return "", err
	}
	query, err := dialect.Rebind(fmt.Sprintf("INSERT INTO %s (name, owner) VALUES (?, ?)", table))
	if err != nil {
		return "", err
	}
	if _, err := r.DB.ExecContext(ctx, query, defaultMigrationLockName, owner); err != nil {
		return "", fmt.Errorf("migration lock is already held: %w", err)
	}
	return owner, nil
}

func newMigrationLockOwner() (string, error) {
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", fmt.Errorf("generate migration lock owner: %w", err)
	}
	return hex.EncodeToString(token[:]), nil
}

func (r *MigrationRunner) releaseLockBounded(table, owner string, dialect MigrationDialect) error {
	ctx, cancel := context.WithTimeout(context.Background(), defaultMigrationReleaseTimeout)
	defer cancel()
	return r.releaseLock(ctx, table, owner, dialect)
}

func (r *MigrationRunner) releaseLock(ctx context.Context, table, owner string, dialect MigrationDialect) error {
	query, err := dialect.Rebind(fmt.Sprintf("DELETE FROM %s WHERE name = ? AND owner = ?", table))
	if err != nil {
		return err
	}
	result, err := r.DB.ExecContext(ctx, query, defaultMigrationLockName, owner)
	if err != nil {
		return fmt.Errorf("release migration lock: %w", err)
	}
	released, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect migration lock release: %w", err)
	}
	if released != 1 {
		return fmt.Errorf("%w: lock %q is not owned by %q", ErrMigrationLockOwnerMismatch, defaultMigrationLockName, owner)
	}
	return nil
}

func (r *MigrationRunner) isApplied(ctx context.Context, table string, version string, dialect MigrationDialect) (bool, error) {
	var dirty bool
	query, err := dialect.Rebind(fmt.Sprintf("SELECT dirty FROM %s WHERE version = ?", table))
	if err != nil {
		return false, err
	}
	err = r.DB.QueryRowContext(ctx, query, version).Scan(&dirty)
	if err == nil {
		if dirty {
			return false, fmt.Errorf("%w: version %s", ErrDirtyMigration, version)
		}
		return true, nil
	}
	if err == sql.ErrNoRows {
		return false, nil
	}
	return false, err
}

func (r *MigrationRunner) rejectDirtyMigration(ctx context.Context, table string) error {
	query := fmt.Sprintf("SELECT version, name FROM %s WHERE dirty = TRUE ORDER BY version LIMIT 1", table)
	var version string
	var name string
	err := r.DB.QueryRowContext(ctx, query).Scan(&version, &name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("check dirty migration state: %w", err)
	}
	return fmt.Errorf("%w: dirty migration %s_%s blocks Up/Down; inspect the schema, then call ForceMigrationState", ErrDirtyMigration, version, name)
}

func (r *MigrationRunner) applyUp(ctx context.Context, table string, migration Migration, dialect MigrationDialect) error {
	if dialect == MigrationDialectMySQL {
		return r.applyUpNonTransactional(ctx, table, migration, dialect)
	}
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
	insert, err := dialect.Rebind(fmt.Sprintf("INSERT INTO %s (version, name, dirty) VALUES (?, ?, FALSE)", table))
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, insert, migration.Version, migration.Name); err != nil {
		return fmt.Errorf("record migration %s_%s: %w", migration.Version, migration.Name, err)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

func (r *MigrationRunner) applyUpNonTransactional(ctx context.Context, table string, migration Migration, dialect MigrationDialect) error {
	insert, err := dialect.Rebind(fmt.Sprintf("INSERT INTO %s (version, name, dirty) VALUES (?, ?, TRUE)", table))
	if err != nil {
		return err
	}
	if _, err := r.DB.ExecContext(ctx, insert, migration.Version, migration.Name); err != nil {
		return fmt.Errorf("mark migration %s_%s dirty: %w", migration.Version, migration.Name, err)
	}
	if _, err := r.DB.ExecContext(ctx, migration.UpSQL); err != nil {
		return fmt.Errorf("apply migration %s_%s: %w", migration.Version, migration.Name, err)
	}
	clear, err := dialect.Rebind(fmt.Sprintf("UPDATE %s SET dirty = FALSE, applied_at = CURRENT_TIMESTAMP WHERE version = ? AND dirty = TRUE", table))
	if err != nil {
		return err
	}
	result, err := r.DB.ExecContext(ctx, clear, migration.Version)
	if err != nil {
		return fmt.Errorf("clear migration %s_%s dirty state: %w", migration.Version, migration.Name, err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect migration %s_%s dirty state: %w", migration.Version, migration.Name, err)
	}
	if changed != 1 {
		return fmt.Errorf("clear migration %s_%s dirty state: history row changed concurrently", migration.Version, migration.Name)
	}
	return nil
}

type appliedMigration struct {
	Version string
	Name    string
}

func (r *MigrationRunner) latestApplied(ctx context.Context, table string, steps int, dialect MigrationDialect) ([]appliedMigration, error) {
	query, err := dialect.Rebind(fmt.Sprintf("SELECT version, name FROM %s WHERE dirty = FALSE ORDER BY version DESC LIMIT ?", table))
	if err != nil {
		return nil, err
	}
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

func (r *MigrationRunner) applyDown(ctx context.Context, table string, migration Migration, dialect MigrationDialect) error {
	if dialect == MigrationDialectMySQL {
		return r.applyDownNonTransactional(ctx, table, migration, dialect)
	}
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
	deleteRecord, err := dialect.Rebind(fmt.Sprintf("DELETE FROM %s WHERE version = ? AND dirty = FALSE", table))
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, deleteRecord, migration.Version); err != nil {
		return fmt.Errorf("remove migration record %s_%s: %w", migration.Version, migration.Name, err)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

func (r *MigrationRunner) applyDownNonTransactional(ctx context.Context, table string, migration Migration, dialect MigrationDialect) error {
	mark, err := dialect.Rebind(fmt.Sprintf("UPDATE %s SET dirty = TRUE WHERE version = ? AND dirty = FALSE", table))
	if err != nil {
		return err
	}
	result, err := r.DB.ExecContext(ctx, mark, migration.Version)
	if err != nil {
		return fmt.Errorf("mark rollback migration %s_%s dirty: %w", migration.Version, migration.Name, err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect rollback migration %s_%s dirty state: %w", migration.Version, migration.Name, err)
	}
	if changed != 1 {
		return fmt.Errorf("mark rollback migration %s_%s dirty: clean history row not found", migration.Version, migration.Name)
	}
	if _, err := r.DB.ExecContext(ctx, migration.DownSQL); err != nil {
		return fmt.Errorf("rollback migration %s_%s: %w", migration.Version, migration.Name, err)
	}
	remove, err := dialect.Rebind(fmt.Sprintf("DELETE FROM %s WHERE version = ? AND dirty = TRUE", table))
	if err != nil {
		return err
	}
	result, err = r.DB.ExecContext(ctx, remove, migration.Version)
	if err != nil {
		return fmt.Errorf("remove rollback migration %s_%s dirty record: %w", migration.Version, migration.Name, err)
	}
	changed, err = result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect rollback migration %s_%s removal: %w", migration.Version, migration.Name, err)
	}
	if changed != 1 {
		return fmt.Errorf("remove rollback migration %s_%s dirty record: history row changed concurrently", migration.Version, migration.Name)
	}
	return nil
}

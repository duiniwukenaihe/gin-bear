package bear

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"reflect"
	"strings"
	"testing"
)

var migrationRunnerLegacyConstructor func(*sql.DB) *MigrationRunner = NewMigrationRunner

type migrationDialectTestConnector struct {
	driver driver.Driver
}

func (c migrationDialectTestConnector) Connect(context.Context) (driver.Conn, error) {
	return nil, errors.New("dialect inference connector does not open connections")
}

func (c migrationDialectTestConnector) Driver() driver.Driver { return c.driver }

type mysqlInferenceDriver struct{}

func (mysqlInferenceDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("not implemented")
}

type pgxInferenceDriver struct{}

func (pgxInferenceDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("not implemented")
}

func TestMigrationRunnerPublicShapeRemainsSourceCompatible(t *testing.T) {
	_ = MigrationRunner{nil, "schema_migrations", "schema_migration_locks"}
	runnerType := reflect.TypeOf(MigrationRunner{})
	if runnerType.NumField() != 3 {
		t.Fatalf("MigrationRunner field count = %d, want 3", runnerType.NumField())
	}
	for index, name := range []string{"DB", "Table", "LockTable"} {
		if field := runnerType.Field(index); field.Name != name || field.PkgPath != "" {
			t.Fatalf("MigrationRunner field %d = %#v, want exported %s", index, field, name)
		}
	}
	if migrationRunnerLegacyConstructor == nil {
		t.Fatal("legacy constructor is nil")
	}
}

func TestNewMigrationRunnerWithDialectKeepsDialectOutsidePublicStruct(t *testing.T) {
	db := newMigrationTestDB(t)
	runner := NewMigrationRunnerWithDialect(db, MigrationDialectPostgreSQL)
	query, err := runner.rebind("SELECT ?, ?")
	if err != nil {
		t.Fatalf("rebind explicit dialect: %v", err)
	}
	if query != "SELECT $1, $2" {
		t.Fatalf("explicit PostgreSQL rebind = %q", query)
	}
}

func TestLegacyRunnerInfersKnownDatabaseDrivers(t *testing.T) {
	mysqlDB := sql.OpenDB(migrationDialectTestConnector{driver: mysqlInferenceDriver{}})
	t.Cleanup(func() { _ = mysqlDB.Close() })
	if dialect := NewMigrationRunner(mysqlDB).dialect(); dialect != MigrationDialectMySQL {
		t.Fatalf("legacy MySQL dialect = %q", dialect)
	}

	postgresDB := sql.OpenDB(migrationDialectTestConnector{driver: pgxInferenceDriver{}})
	t.Cleanup(func() { _ = postgresDB.Close() })
	runner := &MigrationRunner{DB: postgresDB, Table: defaultMigrationTable, LockTable: defaultMigrationLockTable}
	query, err := runner.rebind("SELECT ?")
	if err != nil {
		t.Fatalf("direct literal PostgreSQL rebind: %v", err)
	}
	if query != "SELECT $1" {
		t.Fatalf("direct literal PostgreSQL rebind = %q", query)
	}
}

func TestMigrationDialectRebindsRunnerQueries(t *testing.T) {
	queries := map[string]string{
		"acquire":              "INSERT INTO schema_migration_locks (name, owner) VALUES (?, ?)",
		"release":              "DELETE FROM schema_migration_locks WHERE name = ? AND owner = ?",
		"history lookup":       "SELECT dirty FROM schema_migrations WHERE version = ?",
		"history insert clean": "INSERT INTO schema_migrations (version, name, dirty) VALUES (?, ?, FALSE)",
		"history insert dirty": "INSERT INTO schema_migrations (version, name, dirty) VALUES (?, ?, TRUE)",
		"history latest":       "SELECT version, name FROM schema_migrations WHERE dirty = FALSE ORDER BY version DESC LIMIT ?",
		"history delete clean": "DELETE FROM schema_migrations WHERE version = ? AND dirty = FALSE",
		"history delete dirty": "DELETE FROM schema_migrations WHERE version = ? AND dirty = TRUE",
		"history mark dirty":   "UPDATE schema_migrations SET dirty = TRUE WHERE version = ? AND dirty = FALSE",
		"history clear dirty":  "UPDATE schema_migrations SET dirty = FALSE WHERE version = ? AND dirty = TRUE",
		"force lookup":         "SELECT owner FROM schema_migration_locks WHERE name = ?",
		"force delete":         "DELETE FROM schema_migration_locks WHERE name = ? AND owner = ?",
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
				"acquire":              "INSERT INTO schema_migration_locks (name, owner) VALUES ($1, $2)",
				"release":              "DELETE FROM schema_migration_locks WHERE name = $1 AND owner = $2",
				"history lookup":       "SELECT dirty FROM schema_migrations WHERE version = $1",
				"history insert clean": "INSERT INTO schema_migrations (version, name, dirty) VALUES ($1, $2, FALSE)",
				"history insert dirty": "INSERT INTO schema_migrations (version, name, dirty) VALUES ($1, $2, TRUE)",
				"history latest":       "SELECT version, name FROM schema_migrations WHERE dirty = FALSE ORDER BY version DESC LIMIT $1",
				"history delete clean": "DELETE FROM schema_migrations WHERE version = $1 AND dirty = FALSE",
				"history delete dirty": "DELETE FROM schema_migrations WHERE version = $1 AND dirty = TRUE",
				"history mark dirty":   "UPDATE schema_migrations SET dirty = TRUE WHERE version = $1 AND dirty = FALSE",
				"history clear dirty":  "UPDATE schema_migrations SET dirty = FALSE WHERE version = $1 AND dirty = TRUE",
				"force lookup":         "SELECT owner FROM schema_migration_locks WHERE name = $1",
				"force delete":         "DELETE FROM schema_migration_locks WHERE name = $1 AND owner = $2",
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
	runner := NewMigrationRunnerWithDialect(db, MigrationDialectSQLite)
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

	runner := NewMigrationRunnerWithDialect(db, MigrationDialectSQLite)
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

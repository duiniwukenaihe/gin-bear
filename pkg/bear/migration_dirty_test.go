package bear

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestMySQLApplyUpPersistsDirtyAroundDDL(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("open sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	runner := NewMigrationRunnerWithDialect(db, MigrationDialectMySQL)
	migration := Migration{Version: "001", Name: "create_users", UpSQL: "CREATE TABLE users (id BIGINT PRIMARY KEY)"}

	mock.ExpectExec("INSERT INTO schema_migrations (version, name, dirty) VALUES (?, ?, TRUE)").
		WithArgs("001", "create_users").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(migration.UpSQL).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("UPDATE schema_migrations SET dirty = FALSE, applied_at = CURRENT_TIMESTAMP WHERE version = ? AND dirty = TRUE").
		WithArgs("001").
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := runner.runner.applyUp(context.Background(), defaultMigrationTable, migration, MigrationDialectMySQL); err != nil {
		t.Fatalf("apply MySQL migration: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("MySQL dirty ordering: %v", err)
	}
}

func TestMySQLApplyUpDDLFailureDoesNotClearDirty(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("open sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	runner := NewMigrationRunnerWithDialect(db, MigrationDialectMySQL)
	migration := Migration{Version: "002", Name: "broken", UpSQL: "ALTER TABLE users BROKEN"}
	dllErr := errors.New("mysql ddl failed")

	mock.ExpectExec("INSERT INTO schema_migrations (version, name, dirty) VALUES (?, ?, TRUE)").
		WithArgs("002", "broken").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(migration.UpSQL).WillReturnError(dllErr)

	err = runner.runner.applyUp(context.Background(), defaultMigrationTable, migration, MigrationDialectMySQL)
	if !errors.Is(err, dllErr) {
		t.Fatalf("apply MySQL broken migration error = %v, want DDL cause", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("dirty row must remain after DDL failure: %v", err)
	}
}

func TestMySQLApplyUpFinalizeFailureLeavesDirty(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("open sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	runner := NewMigrationRunnerWithDialect(db, MigrationDialectMySQL)
	migration := Migration{Version: "002", Name: "finalize", UpSQL: "ALTER TABLE users ADD COLUMN email TEXT"}
	finalizeErr := errors.New("history update failed")

	mock.ExpectExec("INSERT INTO schema_migrations (version, name, dirty) VALUES (?, ?, TRUE)").
		WithArgs("002", "finalize").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(migration.UpSQL).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("UPDATE schema_migrations SET dirty = FALSE, applied_at = CURRENT_TIMESTAMP WHERE version = ? AND dirty = TRUE").
		WithArgs("002").
		WillReturnError(finalizeErr)

	err = runner.runner.applyUp(context.Background(), defaultMigrationTable, migration, MigrationDialectMySQL)
	if !errors.Is(err, finalizeErr) {
		t.Fatalf("MySQL finalize error = %v, want history cause", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("dirty row must remain after finalize failure: %v", err)
	}
}

func TestMySQLApplyDownPersistsDirtyAroundDDL(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("open sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	runner := NewMigrationRunnerWithDialect(db, MigrationDialectMySQL)
	migration := Migration{Version: "003", Name: "drop_users", DownSQL: "DROP TABLE users"}

	mock.ExpectExec("UPDATE schema_migrations SET dirty = TRUE WHERE version = ? AND dirty = FALSE").
		WithArgs("003").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(migration.DownSQL).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("DELETE FROM schema_migrations WHERE version = ? AND dirty = TRUE").
		WithArgs("003").
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := runner.runner.applyDown(context.Background(), defaultMigrationTable, migration, MigrationDialectMySQL); err != nil {
		t.Fatalf("rollback MySQL migration: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("MySQL rollback dirty ordering: %v", err)
	}
}

func TestMySQLApplyDownSQLFailureKeepsDirtyForOperatorSelectedState(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("open sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	runner := NewMigrationRunnerWithDialect(db, MigrationDialectMySQL)
	migration := Migration{
		Version: "003",
		Name:    "partially_executed_down",
		DownSQL: "DROP TABLE users; DROP TABLE audit_logs",
	}
	downErr := errors.New("mysql second rollback statement failed")

	mock.ExpectExec("UPDATE schema_migrations SET dirty = TRUE WHERE version = ? AND dirty = FALSE").
		WithArgs("003").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(migration.DownSQL).WillReturnError(downErr)

	err = runner.runner.applyDown(context.Background(), defaultMigrationTable, migration, MigrationDialectMySQL)
	if !errors.Is(err, downErr) {
		t.Fatalf("rollback MySQL broken migration error = %v, want SQL cause", err)
	}
	for _, guidance := range []string{"may have partially executed", "inspect the schema", "applied true or false"} {
		if !strings.Contains(err.Error(), guidance) {
			t.Fatalf("rollback MySQL broken migration error = %v, want guidance %q", err, guidance)
		}
	}
	for _, fixedDirection := range []string{
		`ForceMigrationState(ctx, "003", true)`,
		`ForceMigrationState(ctx, "003", false)`,
	} {
		if strings.Contains(err.Error(), fixedDirection) {
			t.Fatalf("rollback MySQL broken migration error fixed recovery direction %q: %v", fixedDirection, err)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("dirty row must remain after rollback DDL failure: %v", err)
	}
}

func TestMySQLApplyDownHistoryDeleteFailureRequiresDirtyRecordRemoval(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("open sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	runner := NewMigrationRunnerWithDialect(db, MigrationDialectMySQL)
	migration := Migration{Version: "008", Name: "down_completed", DownSQL: "DROP TABLE users"}
	deleteErr := errors.New("history delete failed")

	mock.ExpectExec("UPDATE schema_migrations SET dirty = TRUE WHERE version = ? AND dirty = FALSE").
		WithArgs("008").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(migration.DownSQL).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("DELETE FROM schema_migrations WHERE version = ? AND dirty = TRUE").
		WithArgs("008").
		WillReturnError(deleteErr)

	err = runner.runner.applyDown(context.Background(), defaultMigrationTable, migration, MigrationDialectMySQL)
	if !errors.Is(err, deleteErr) {
		t.Fatalf("MySQL history delete error = %v, want delete cause", err)
	}
	if !strings.Contains(err.Error(), `ForceMigrationState(ctx, "008", false)`) {
		t.Fatalf("MySQL history delete error = %v, want dirty-record removal guidance", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("dirty row must remain after history delete failure: %v", err)
	}
}

func TestForceMigrationStateRestoresAppliedRecordWhenMySQLDownFails(t *testing.T) {
	db := newMigrationTestDB(t)
	runner := NewMigrationRunnerWithDialect(db, MigrationDialectMySQL)
	up := Migration{Version: "009", Name: "down_failed", UpSQL: "CREATE TABLE down_failed_target (id INTEGER PRIMARY KEY)"}
	if err := runner.Up(context.Background(), []Migration{up}); err != nil {
		t.Fatalf("apply migration before failed Down: %v", err)
	}
	down := up
	down.DownSQL = "DROP TABLE table_that_does_not_exist"
	if err := runner.Down(context.Background(), []Migration{down}, 1); err == nil {
		t.Fatal("broken Down SQL should fail")
	}

	if err := runner.ForceMigrationState(context.Background(), "009", true); err != nil {
		t.Fatalf("restore failed Down as applied: %v", err)
	}
	var dirty bool
	if err := db.QueryRowContext(context.Background(), "SELECT dirty FROM schema_migrations WHERE version = ?", "009").Scan(&dirty); err != nil {
		t.Fatalf("read restored applied record: %v", err)
	}
	if dirty {
		t.Fatal("failed Down recovery left applied record dirty")
	}
	assertSQLiteTableCount(t, db, "down_failed_target", 1)
}

func TestForceMigrationStateRemovesDirtyRecordWhenMySQLDownCompleted(t *testing.T) {
	db := newMigrationTestDB(t)
	runner := NewMigrationRunnerWithDialect(db, MigrationDialectMySQL)
	migration := Migration{
		Version: "010",
		Name:    "down_completed",
		UpSQL:   "CREATE TABLE down_completed_target (id INTEGER PRIMARY KEY)",
		DownSQL: "DROP TABLE down_completed_target",
	}
	if err := runner.Up(context.Background(), []Migration{migration}); err != nil {
		t.Fatalf("apply migration before completed Down: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `CREATE TRIGGER fail_migration_history_delete
BEFORE DELETE ON schema_migrations
BEGIN
    SELECT RAISE(FAIL, 'history delete failed');
END`); err != nil {
		t.Fatalf("install history delete failure: %v", err)
	}
	if err := runner.Down(context.Background(), []Migration{migration}, 1); err == nil || !strings.Contains(err.Error(), "history delete failed") {
		t.Fatalf("completed Down history failure = %v", err)
	}
	if _, err := db.ExecContext(context.Background(), "DROP TRIGGER fail_migration_history_delete"); err != nil {
		t.Fatalf("remove history delete failure: %v", err)
	}

	if err := runner.ForceMigrationState(context.Background(), "010", false); err != nil {
		t.Fatalf("remove dirty record after completed Down: %v", err)
	}
	var historyCount int
	if err := db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM schema_migrations WHERE version = ?", "010").Scan(&historyCount); err != nil {
		t.Fatalf("count removed history record: %v", err)
	}
	if historyCount != 0 {
		t.Fatalf("completed Down history count = %d, want 0", historyCount)
	}
	assertSQLiteTableCount(t, db, "down_completed_target", 0)
}

func assertSQLiteTableCount(t *testing.T, db interface {
	QueryRowContext(context.Context, string, ...interface{}) *sql.Row
}, table string, want int) {
	t.Helper()
	var count int
	if err := db.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?",
		table,
	).Scan(&count); err != nil {
		t.Fatalf("look up SQLite table %s: %v", table, err)
	}
	if count != want {
		t.Fatalf("SQLite table %s count = %d, want %d", table, count, want)
	}
}

func TestPostgreSQLMarkedApplyUpPersistsDirtyAroundDDL(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("open sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	migration := Migration{
		Version: "006",
		Name:    "users_email_index",
		UpSQL:   MigrationNonTransactionalDirective + "\nCREATE INDEX CONCURRENTLY users_email_idx ON users (email)",
	}

	mock.ExpectExec("INSERT INTO schema_migrations (version, name, dirty) VALUES ($1, $2, TRUE)").
		WithArgs("006", "users_email_index").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(migration.UpSQL).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("UPDATE schema_migrations SET dirty = FALSE, applied_at = CURRENT_TIMESTAMP WHERE version = $1 AND dirty = TRUE").
		WithArgs("006").
		WillReturnResult(sqlmock.NewResult(0, 1))

	runner := NewMigrationRunnerWithDialect(db, MigrationDialectPostgreSQL)
	if err := runner.runner.applyUp(context.Background(), defaultMigrationTable, migration, MigrationDialectPostgreSQL); err != nil {
		t.Fatalf("apply marked PostgreSQL migration: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("PostgreSQL marked dirty ordering: %v", err)
	}
}

func TestPostgreSQLMarkedApplyDownPersistsDirtyAroundDDL(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("open sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	migration := Migration{
		Version: "006",
		Name:    "users_email_index",
		DownSQL: MigrationNonTransactionalDirective + "\nDROP INDEX CONCURRENTLY users_email_idx",
	}

	mock.ExpectExec("UPDATE schema_migrations SET dirty = TRUE WHERE version = $1 AND dirty = FALSE").
		WithArgs("006").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(migration.DownSQL).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("DELETE FROM schema_migrations WHERE version = $1 AND dirty = TRUE").
		WithArgs("006").
		WillReturnResult(sqlmock.NewResult(0, 1))

	runner := NewMigrationRunnerWithDialect(db, MigrationDialectPostgreSQL)
	if err := runner.runner.applyDown(context.Background(), defaultMigrationTable, migration, MigrationDialectPostgreSQL); err != nil {
		t.Fatalf("rollback marked PostgreSQL migration: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("PostgreSQL marked rollback dirty ordering: %v", err)
	}
}

func TestPostgreSQLConcurrentIndexWithoutDirectiveRemainsTransactional(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("open sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	migration := Migration{
		Version: "007",
		Name:    "unmarked_index",
		UpSQL:   "CREATE INDEX CONCURRENTLY users_name_idx ON users (name)",
	}

	mock.ExpectBegin()
	mock.ExpectExec(migration.UpSQL).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("INSERT INTO schema_migrations (version, name, dirty) VALUES ($1, $2, FALSE)").
		WithArgs("007", "unmarked_index").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	runner := NewMigrationRunnerWithDialect(db, MigrationDialectPostgreSQL)
	if err := runner.runner.applyUp(context.Background(), defaultMigrationTable, migration, MigrationDialectPostgreSQL); err != nil {
		t.Fatalf("apply unmarked PostgreSQL migration: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("PostgreSQL must not infer non-transactional SQL: %v", err)
	}
}

func TestMySQLDirtyMigrationBlocksUpAndDownUntilForced(t *testing.T) {
	db := newMigrationTestDB(t)
	runner := NewMigrationRunnerWithDialect(db, MigrationDialectMySQL)
	broken := Migration{Version: "004", Name: "broken", UpSQL: "CREATE TABLE broken ("}
	if err := runner.Up(context.Background(), []Migration{broken}); err == nil {
		t.Fatal("broken MySQL DDL should fail")
	}

	for name, run := range map[string]func() error{
		"up": func() error { return runner.Up(context.Background(), []Migration{broken}) },
		"down": func() error {
			return runner.Down(context.Background(), []Migration{{Version: "004", Name: "broken", DownSQL: "DROP TABLE broken"}}, 1)
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := run()
			if err == nil || !strings.Contains(err.Error(), "dirty migration 004_broken") || !strings.Contains(err.Error(), "ForceMigrationState") {
				t.Fatalf("blocked %s error = %v", name, err)
			}
		})
	}

	if err := runner.ForceMigrationState(context.Background(), "004", false); err != nil {
		t.Fatalf("force dirty migration unapplied: %v", err)
	}
	fixed := Migration{Version: "004", Name: "broken", UpSQL: "CREATE TABLE broken (id BIGINT PRIMARY KEY)"}
	if err := runner.Up(context.Background(), []Migration{fixed}); err != nil {
		t.Fatalf("up after force recovery: %v", err)
	}
}

func TestForceMigrationStateCanConfirmDirtyVersionApplied(t *testing.T) {
	db := newMigrationTestDB(t)
	runner := NewMigrationRunnerWithDialect(db, MigrationDialectMySQL)
	broken := Migration{Version: "005", Name: "completed_before_history", UpSQL: "CREATE TABLE forced_applied ("}
	if err := runner.Up(context.Background(), []Migration{broken}); err == nil {
		t.Fatal("broken MySQL migration should leave dirty state")
	}

	if err := runner.ForceMigrationState(context.Background(), "005", true); err != nil {
		t.Fatalf("force dirty migration applied: %v", err)
	}
	var dirty bool
	if err := db.QueryRowContext(context.Background(),
		"SELECT dirty FROM schema_migrations WHERE version = ?",
		"005",
	).Scan(&dirty); err != nil {
		t.Fatalf("read forced applied state: %v", err)
	}
	if dirty {
		t.Fatal("forced applied migration remained dirty")
	}
	if err := runner.Up(context.Background(), []Migration{broken}); err != nil {
		t.Fatalf("forced applied migration should be skipped: %v", err)
	}
}

func TestEnsureMigrationTableUpgradesLegacySchemaWithDirty(t *testing.T) {
	db := newMigrationTestDB(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `CREATE TABLE schema_migrations (
version TEXT PRIMARY KEY,
name TEXT NOT NULL,
applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
)`); err != nil {
		t.Fatalf("create legacy migration table: %v", err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO schema_migrations (version, name) VALUES (?, ?)", "001", "legacy"); err != nil {
		t.Fatalf("insert legacy migration: %v", err)
	}

	runner := NewMigrationRunner(db)
	if err := runner.ensureTable(ctx, defaultMigrationTable, MigrationDialectSQLite); err != nil {
		t.Fatalf("upgrade legacy migration table: %v", err)
	}
	var dirty bool
	if err := db.QueryRowContext(ctx, "SELECT dirty FROM schema_migrations WHERE version = ?", "001").Scan(&dirty); err != nil {
		t.Fatalf("read upgraded dirty state: %v", err)
	}
	if dirty {
		t.Fatal("legacy completed migration was marked dirty")
	}
}

package bear

import (
	"context"
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

func TestMySQLApplyDownDDLFailureDoesNotDeleteDirty(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("open sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	runner := NewMigrationRunnerWithDialect(db, MigrationDialectMySQL)
	migration := Migration{Version: "003", Name: "broken_down", DownSQL: "DROP TABLE users"}
	dllErr := errors.New("mysql rollback ddl failed")

	mock.ExpectExec("UPDATE schema_migrations SET dirty = TRUE WHERE version = ? AND dirty = FALSE").
		WithArgs("003").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(migration.DownSQL).WillReturnError(dllErr)

	err = runner.runner.applyDown(context.Background(), defaultMigrationTable, migration, MigrationDialectMySQL)
	if !errors.Is(err, dllErr) {
		t.Fatalf("rollback MySQL broken migration error = %v, want DDL cause", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("dirty row must remain after rollback DDL failure: %v", err)
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

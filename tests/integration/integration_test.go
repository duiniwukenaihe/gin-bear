package integration

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/duiniwukenaihe/gin-bear/internal/scaffold"
	"github.com/duiniwukenaihe/gin-bear/pkg/bear"
)

// Integration evidence gate.
//
// These tests never run as part of the default unit suite: without
// BEAR_INTEGRATION=1 every test skips immediately without dialing anything,
// so `go test ./...`, `go vet ./...` and the race job stay hermetic.
// scripts/test-integration.sh is the single supported entrypoint: it prepares
// disposable databases/roles (never development or production ones), exports
// DSNs, runs this package, redacts credentials from the log, and drops
// everything it created.
//
// Available dependencies are recorded, unavailable ones are reported as
// NOT_RUN skips rather than passes:
//
//	BEAR_INTEGRATION=1                  enable (set by the script)
//	BEAR_INTEGRATION_PG_DSN             postgres DSN for the disposable test database
//	BEAR_INTEGRATION_MYSQL_DSN          mysql DSN (optional; absent => MySQL NOT_RUN)
//	BEAR_INTEGRATION_REDIS_ADDR         redis addr (optional; unreachable => Redis NOT_RUN unless required)
func requireIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv("BEAR_INTEGRATION") != "1" {
		t.Skip("integration disabled: run scripts/test-integration.sh")
	}
}

func pgDSN(t *testing.T) (string, bool) {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("BEAR_INTEGRATION_PG_DSN"))
	if dsn == "" {
		t.Log("NOT_RUN: no BEAR_INTEGRATION_PG_DSN")
		return "", false
	}
	return dsn, true
}

func mysqlDSN(t *testing.T) (string, bool) {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("BEAR_INTEGRATION_MYSQL_DSN"))
	if dsn == "" {
		t.Skip("NOT_RUN: no BEAR_INTEGRATION_MYSQL_DSN (no MySQL service in this environment)")
		return "", false
	}
	return dsn, true
}

func redisAddr(t *testing.T) (string, bool) {
	t.Helper()
	addr := strings.TrimSpace(os.Getenv("BEAR_INTEGRATION_REDIS_ADDR"))
	if addr == "" {
		addr = "127.0.0.1:6379"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	adapter, err := bear.OpenRedisAdapterContext(ctx, &bear.RedisConfig{Addr: addr})
	if err != nil {
		for _, engine := range strings.Split(os.Getenv("BEAR_INTEGRATION_REQUIRE"), ",") {
			if strings.TrimSpace(engine) == "redis" {
				t.Fatalf("required redis at %s is unavailable: %v", addr, err)
			}
		}
		t.Logf("NOT_RUN: redis at %s unreachable: %v", addr, err)
		return "", false
	}
	_ = adapter.Shutdown()
	return addr, true
}

func openPG(t *testing.T, dsn string) *bear.GormAdapter {
	t.Helper()
	t.Setenv("BEAR_ENV", "test")
	t.Setenv("GIN_MODE", "")
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse PG DSN: %v", err)
	}
	password, _ := parsed.User.Password()
	cfg := &bear.DBConfig{
		Enabled:  true,
		Type:     "postgres",
		Host:     parsed.Hostname(),
		Port:     parsed.Port(),
		User:     parsed.User.Username(),
		Password: password,
		DBName:   strings.TrimPrefix(parsed.Path, "/"),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	adapter, err := bear.OpenGormAdapter(ctx, cfg)
	if err != nil {
		t.Fatalf("open PG adapter: %v", err)
	}
	t.Cleanup(func() { _ = adapter.Shutdown() })
	var version string
	if err := adapter.DB.Raw("SELECT current_setting('server_version')").Scan(&version).Error; err != nil {
		t.Fatalf("read server_version: %v", err)
	}
	t.Logf("postgres server_version=%s", version)
	return adapter
}

func sqlDB(t *testing.T, adapter *bear.GormAdapter) *sql.DB {
	t.Helper()
	db, err := adapter.DB.DB()
	if err != nil {
		t.Fatalf("sql.DB: %v", err)
	}
	return db
}

// TestIntegrationPostgresMigrationUpDownFailure exercises the real migration
// runner: apply, roll back, then prove a broken file fails loudly without
// recording its version.
func TestIntegrationPostgresMigrationUpDownFailure(t *testing.T) {
	requireIntegration(t)
	dsn, ok := pgDSN(t)
	if !ok {
		t.Skip("NOT_RUN: postgres unavailable")
	}
	adapter := openPG(t, dsn)
	db := sqlDB(t, adapter)

	dir := t.TempDir()
	writeFile(t, dir, "001_create_widget.up.sql", "CREATE TABLE widget (id BIGSERIAL PRIMARY KEY, name varchar(255) NOT NULL);\n")
	writeFile(t, dir, "001_create_widget.down.sql", "DROP TABLE IF EXISTS widget;\n")
	writeFile(t, dir, "002_create_gadget.up.sql", "CREATE TABLE gadget (id BIGSERIAL PRIMARY KEY);\n")
	writeFile(t, dir, "002_create_gadget.down.sql", "DROP TABLE IF EXISTS gadget;\n")

	migrations, err := bear.LoadSQLMigrations(dir)
	if err != nil {
		t.Fatalf("LoadSQLMigrations: %v", err)
	}
	runner := bear.NewMigrationRunner(db)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := runner.Up(ctx, migrations); err != nil {
		t.Fatalf("Up: %v", err)
	}
	assertTableExists(t, db, "widget", true)
	assertTableExists(t, db, "gadget", true)

	if err := runner.Down(ctx, migrations, 1); err != nil {
		t.Fatalf("Down: %v", err)
	}
	assertTableExists(t, db, "widget", true)
	assertTableExists(t, db, "gadget", false)

	if err := runner.Up(ctx, migrations); err != nil {
		t.Fatalf("re-Up: %v", err)
	}

	writeFile(t, dir, "003_broken.up.sql", "CREATE TABLE broken (id THIS_IS_NOT_A_TYPE);\n")
	writeFile(t, dir, "003_broken.down.sql", "DROP TABLE IF EXISTS broken;\n")
	broken, err := bear.LoadSQLMigrations(dir)
	if err != nil {
		t.Fatalf("LoadSQLMigrations with broken file: %v", err)
	}
	if err := runner.Up(ctx, broken); err == nil {
		t.Fatal("Up with broken migration succeeded, want failure")
	} else if !strings.Contains(err.Error(), "003_broken") {
		t.Fatalf("Up error %v does not name the broken file", err)
	}
	assertTableExists(t, db, "broken", false)
}

// TestIntegrationPostgresGeneratedAppCRUD boots a genuinely generated
// application against the disposable PostgreSQL database and performs one
// create/read round trip over HTTP.
func TestIntegrationPostgresGeneratedAppCRUD(t *testing.T) {
	requireIntegration(t)
	dsn, ok := pgDSN(t)
	if !ok {
		t.Skip("NOT_RUN: postgres unavailable")
	}

	project := filepath.Join(t.TempDir(), "pg-app")
	if err := scaffold.Generate(context.Background(), scaffold.Options{
		Name:             "pg-app",
		Module:           "example.com/pg-app",
		Directory:        project,
		FrameworkVersion: "v0.0.0",
		FrameworkReplace: repoRoot(t),
	}); err != nil {
		t.Fatal(err)
	}
	writePGAppConfig(t, project, dsn)

	bearBinary := buildRepoCLI(t)
	stdout, stderr, code := runBinary(t, project, bearBinary, "gen", "api", "widget", "--fields", "name:string")
	if code != 0 {
		t.Fatalf("gen api failed (%d):\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	runGo(t, project, "mod", "tidy")
	runGo(t, project, "run", "./cmd/migrate")

	port := freePort(t)
	serverBinary := filepath.Join(t.TempDir(), "pg-server")
	if runtime.GOOS == "windows" {
		serverBinary += ".exe"
	}
	runGo(t, project, "build", "-o", serverBinary, "./cmd/server")
	var output bytes.Buffer
	cmd := exec.Command(serverBinary)
	cmd.Dir = project
	cmd.Env = append(os.Environ(), fmt.Sprintf("BEAR_SERVER_PORT=%d", port))
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	client := &http.Client{Timeout: 10 * time.Second}
	waitForLive(t, client, base+"/live", &output)
	created := jsonRequest(t, client, http.MethodPost, base+"/api/v1/widget", `{"name":"pg-first"}`, &output)
	if created["code"] != float64(http.StatusCreated) {
		t.Fatalf("POST widget = %v\nserver output:\n%s", created, output.String())
	}
	list := jsonRequest(t, client, http.MethodGet, base+"/api/v1/widget", "", &output)
	payload, ok := list["data"].(map[string]any)
	if !ok {
		t.Fatalf("GET widget returned no data: %v", list)
	}
	if total, _ := payload["total"].(float64); total != 1 {
		t.Fatalf("GET widget total = %v, want 1", payload["total"])
	}

	// Graceful shutdown: TERM must drain and exit.
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("signal server: %v", err)
	}
	done := make(chan error, 1)
	go func() { _, err := cmd.Process.Wait(); done <- err }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("generated server did not exit after interrupt")
	}
}

// TestIntegrationPostgresTransactionRollbackCancelInterruption covers the
// failure semantics the unit suite can only model on SQLite: real rollback,
// blocking-query cancellation with pool reuse, and backend kill recovery.
func TestIntegrationPostgresTransactionRollbackCancelInterruption(t *testing.T) {
	requireIntegration(t)
	dsn, ok := pgDSN(t)
	if !ok {
		t.Skip("NOT_RUN: postgres unavailable")
	}
	adapter := openPG(t, dsn)
	db := sqlDB(t, adapter)
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, "CREATE TEMPORARY TABLE int_tx (id BIGSERIAL PRIMARY KEY, name varchar(255))"); err != nil {
		t.Fatalf("create temp table: %v", err)
	}

	t.Run("rollback", func(t *testing.T) {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO int_tx (name) VALUES ('tx-row')"); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Rollback(); err != nil {
			t.Fatal(err)
		}
		var count int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM int_tx WHERE name='tx-row'").Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("count after rollback = %d, want 0", count)
		}
	})

	t.Run("cancel", func(t *testing.T) {
		cancelCtx, cancel := context.WithCancel(context.Background())
		cancel()
		started := time.Now()
		err := adapter.DB.WithContext(cancelCtx).Exec("SELECT pg_sleep(30)").Error
		elapsed := time.Since(started)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("blocked query err = %v, want context.Canceled (elapsed %s)", err, elapsed)
		}
		t.Logf("blocked query canceled after %s", elapsed)
		var one int
		if err := adapter.DB.WithContext(ctx).Raw("SELECT 1").Scan(&one).Error; err != nil || one != 1 {
			t.Fatalf("pool reuse after cancel: value=%d err=%v", one, err)
		}
	})

	t.Run("interruption", func(t *testing.T) {
		conn, err := db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		var pid int
		if err := conn.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, "SELECT pg_terminate_backend($1)", pid); err != nil {
			t.Fatal(err)
		}
		var one int
		deadline, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		if err := adapter.DB.WithContext(deadline).Raw("SELECT 1").Scan(&one).Error; err != nil || one != 1 {
			t.Fatalf("query after backend kill: value=%d err=%v", one, err)
		}
	})
}

// TestIntegrationPostgresRevocationReload proves that a revocation committed
// by one instance is visible on the next authorization of another instance.
func TestIntegrationPostgresRevocationReload(t *testing.T) {
	requireIntegration(t)
	dsn, ok := pgDSN(t)
	if !ok {
		t.Skip("NOT_RUN: postgres unavailable")
	}
	first := openPG(t, dsn)
	second := openPG(t, dsn)
	migrations, err := bear.LoadSQLMigrations(filepath.Join("..", "..", "migrations", "optional", "casbin-postgres"))
	if err != nil {
		t.Fatalf("load Casbin PostgreSQL migration: %v", err)
	}
	runner := bear.NewMigrationRunnerWithDialect(sqlDB(t, first), bear.MigrationDialectPostgreSQL).
		ConfigureTables("casbin_pg_schema_migrations_test", "casbin_pg_migration_locks_test")
	if err := runner.Up(context.Background(), migrations); err != nil {
		t.Fatalf("apply Casbin PostgreSQL migration: %v", err)
	}
	firstStore, err := bear.NewPostgresCasbinAdapter(sqlDB(t, first))
	if err != nil {
		t.Fatal(err)
	}
	secondStore, err := bear.NewPostgresCasbinAdapter(sqlDB(t, second))
	if err != nil {
		t.Fatal(err)
	}
	a, err := bear.NewCasbinAuthorizerWithAdapter(firstStore, nil)
	if err != nil {
		t.Fatalf("first authorizer: %v", err)
	}
	b, err := bear.NewCasbinAuthorizerWithAdapter(secondStore, nil)
	if err != nil {
		t.Fatalf("second authorizer: %v", err)
	}
	request := bear.AuthorizationRequest{Subject: "alice", Resource: "/secret", Action: "GET"}
	if _, err := a.AddPolicy("admin", "/secret", "GET"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.AddGroupingPolicy("alice", "admin"); err != nil {
		t.Fatal(err)
	}
	if err := b.LoadPolicy(); err != nil {
		t.Fatal(err)
	}
	if allowed, err := b.Authorize(context.Background(), request); err != nil || !allowed {
		t.Fatalf("b after load = %v, %v; want true, nil", allowed, err)
	}
	if _, err := a.RemoveGroupingPolicy("alice", "admin"); err != nil {
		t.Fatal(err)
	}
	if allowed, err := b.Authorize(context.Background(), request); err != nil {
		t.Fatalf("b after remote revocation: %v", err)
	} else if allowed {
		t.Fatal("b after remote revocation = true, want false")
	}
	if _, err := a.AddGroupingPolicy("alice", "admin"); err != nil {
		t.Fatal(err)
	}
	// b last observed the revoked policy; its next mutation must refresh
	// before deciding whether the newly restored rule exists.
	if removed, err := b.RemoveGroupingPolicy("alice", "admin"); err != nil || !removed {
		t.Fatalf("b remote revocation = %v, %v; want true, nil", removed, err)
	}
	if allowed, err := a.Authorize(context.Background(), request); err != nil || allowed {
		t.Fatalf("a after b remote revocation = %v, %v; want false, nil", allowed, err)
	}
}

// TestIntegrationRedisRoundTripAndFailure covers the enabled Redis paths:
// real round trip plus the designed failure behavior (fast explicit error for
// unreachable, revocation unavailable without a client).
func TestIntegrationRedisRoundTripAndFailure(t *testing.T) {
	requireIntegration(t)
	addr, ok := redisAddr(t)
	if !ok {
		t.Skip("NOT_RUN: redis unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	adapter, err := bear.OpenRedisAdapterContext(ctx, &bear.RedisConfig{Addr: addr})
	if err != nil {
		t.Fatalf("open redis: %v", err)
	}
	defer adapter.Shutdown()
	key := fmt.Sprintf("bear:integration:%d", os.Getpid())
	if err := adapter.Client.Set(ctx, key, "alive", time.Minute).Err(); err != nil {
		t.Fatalf("SET: %v", err)
	}
	got, err := adapter.Client.Get(ctx, key).Result()
	if err != nil || got != "alive" {
		t.Fatalf("GET = %q, %v; want alive, nil", got, err)
	}
	_ = adapter.Client.Del(ctx, key).Err()
	if err := adapter.CheckReady(ctx); err != nil {
		t.Fatalf("CheckReady: %v", err)
	}
	info, err := adapter.Client.Info(ctx, "server").Result()
	if err != nil {
		t.Fatalf("INFO: %v", err)
	}
	for _, line := range strings.Split(info, "\n") {
		if strings.HasPrefix(line, "redis_version:") {
			t.Logf("redis %s", strings.TrimSpace(line))
		}
	}

	t.Run("unreachable", func(t *testing.T) {
		started := time.Now()
		failCtx, failCancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer failCancel()
		_, err := bear.OpenRedisAdapterContext(failCtx, &bear.RedisConfig{Addr: "127.0.0.1:1"})
		if err == nil {
			t.Fatal("redis dial to a closed port succeeded, want fast failure")
		}
		if elapsed := time.Since(started); elapsed > 10*time.Second {
			t.Fatalf("unreachable redis took %s, want a bounded failure", elapsed)
		}
	})

	t.Run("revocation-unavailable", func(t *testing.T) {
		manager := bear.NewAuthTokenManager()
		if err := manager.RevokeToken(ctx, "any-token"); !errors.Is(err, bear.ErrTokenRevocationUnavailable) {
			t.Fatalf("RevokeToken without redis = %v, want ErrTokenRevocationUnavailable", err)
		}
	})
}

// TestIntegrationMySQLCore runs the portable core (migrate, CRUD, cancel) when
// a MySQL DSN is provided; otherwise it is an explicit NOT_RUN skip.
func TestIntegrationMySQLCore(t *testing.T) {
	requireIntegration(t)
	dsn, ok := mysqlDSN(t)
	if !ok {
		return
	}
	t.Setenv("BEAR_ENV", "test")
	t.Setenv("GIN_MODE", "")
	parsed, err := url.Parse(strings.Replace(dsn, "mysql://", "http://", 1))
	if err != nil {
		t.Fatalf("parse MySQL DSN: %v", err)
	}
	password, _ := parsed.User.Password()
	cfg := &bear.DBConfig{
		Enabled:  true,
		Type:     "mysql",
		Host:     parsed.Hostname(),
		Port:     parsed.Port(),
		User:     parsed.User.Username(),
		Password: password,
		DBName:   strings.TrimPrefix(parsed.Path, "/"),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	adapter, err := bear.OpenGormAdapter(ctx, cfg)
	if err != nil {
		t.Fatalf("open MySQL adapter: %v", err)
	}
	defer adapter.Shutdown()
	var version string
	if err := adapter.DB.Raw("SELECT VERSION()").Scan(&version).Error; err != nil {
		t.Fatalf("SELECT VERSION(): %v", err)
	}
	t.Logf("mysql version=%s", version)

	db, err := adapter.DB.DB()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	writeFile(t, dir, "001_create_widget.up.sql", "CREATE TABLE widget (id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY, name varchar(255) NOT NULL);\n")
	writeFile(t, dir, "001_create_widget.down.sql", "DROP TABLE IF EXISTS widget;\n")
	migrations, err := bear.LoadSQLMigrations(dir)
	if err != nil {
		t.Fatal(err)
	}
	runner := bear.NewMigrationRunner(db)
	if err := runner.Up(ctx, migrations); err != nil {
		t.Fatalf("Up: %v", err)
	}
	assertMySQLTableExists(t, db, "widget", true)
	if _, err := db.ExecContext(ctx, "INSERT INTO widget (name) VALUES ('m-first')"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM widget").Scan(&count); err != nil || count != 1 {
		t.Fatalf("count = %d, %v; want 1, nil", count, err)
	}
	cancelCtx, cancelFn := context.WithCancel(context.Background())
	cancelFn()
	if err := adapter.DB.WithContext(cancelCtx).Exec("SELECT SLEEP(30)").Error; !errors.Is(err, context.Canceled) {
		t.Fatalf("blocked MySQL query err = %v, want context.Canceled", err)
	}
	if err := runner.Down(ctx, migrations, 1); err != nil {
		t.Fatalf("Down: %v", err)
	}
	assertMySQLTableExists(t, db, "widget", false)
}

func writeFile(t *testing.T, dir, name, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertTableExists(t *testing.T, db *sql.DB, table string, want bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var name sql.NullString
	if err := db.QueryRowContext(ctx, "SELECT to_regclass($1)::text", "public."+table).Scan(&name); err != nil {
		t.Fatalf("table probe %s: %v", table, err)
	}
	if got := name.Valid && name.String != ""; got != want {
		t.Fatalf("table %s exists = %v, want %v", table, got, want)
	}
}

func assertMySQLTableExists(t *testing.T, db *sql.DB, table string, want bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var name string
	err := db.QueryRowContext(ctx, "SELECT TABLE_NAME FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?", table).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
		name = ""
	}
	if err != nil {
		t.Fatalf("table probe %s: %v", table, err)
	}
	if got := name != ""; got != want {
		t.Fatalf("table %s exists = %v, want %v", table, got, want)
	}
}

package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunInitializesEnabledDatabaseBeforeServing(t *testing.T) {
	httpPort := commandUnusedPort(t)
	databasePort := commandUnusedPort(t)
	directory := t.TempDir()
	configuration := fmt.Sprintf(`server:
  port: %d
  shutdown_timeout: 200ms
config:
  strict: true
  framework.strict: true
database:
  enabled: true
  type: postgres
  host: 127.0.0.1
  port: %q
  user: test
  password: test
  dbname: unavailable
  sslmode: disable
health:
  readiness_timeout: 20ms
metrics:
  enabled: false
auth:
  enabled: false
`, httpPort, fmt.Sprint(databasePort))
	if err := os.WriteFile(filepath.Join(directory, "application.yaml"), []byte(configuration), 0o600); err != nil {
		t.Fatal(err)
	}
	previousDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(directory); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previousDirectory) })
	t.Setenv("BEAR_ENV", "dev")
	t.Setenv("GIN_MODE", "debug")

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	err = run(ctx)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "database") {
		t.Fatalf("run() error = %v, want database initialization failure before serving", err)
	}
}

func commandUnusedPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// productionConfigHeader mirrors the shape documented in
// internal/scaffold/template/application-prod.yaml.example.tmpl: strict
// loading, strict framework contracts, release mode, and an explicit proxy
// allow-list. framework.strict is not optional here — Validate() rejects
// production unless it is true or allow_compatibility_in_production is set.
const productionConfigHeader = `server:
  name: migration-example
  mode: "release"
  trusted_proxies:
    - "127.0.0.1"
config:
  strict: true
  framework.strict: true
`

func writeProductionConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "application-prod.yaml")
	if err := os.WriteFile(path, []byte(productionConfigHeader+body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestLoadProductionConfigReturnsParsedConfiguration pins the happy path that
// docs/production.md points readers at: the example really does hand back a
// validated production configuration instead of panicking.
func TestLoadProductionConfigReturnsParsedConfiguration(t *testing.T) {
	t.Setenv("BEAR_ENV", "prod")
	path := writeProductionConfig(t, "database:\n  enabled: false\n")

	config, err := loadProductionConfig(path)
	if err != nil {
		t.Fatalf("loadProductionConfig() error = %v, want a loaded configuration", err)
	}
	if config == nil || config.Server == nil {
		t.Fatalf("loadProductionConfig() config = %+v, want a populated server section", config)
	}
	if config.Server.Name != "migration-example" {
		t.Fatalf("server name = %q, want %q", config.Server.Name, "migration-example")
	}
}

// TestLoadProductionConfigWrapsLoadFailures pins the error-returning contract
// the example exists to demonstrate: failures travel back to the caller as
// wrapped errors so main() can report them and exit, never as a panic.
func TestLoadProductionConfigWrapsLoadFailures(t *testing.T) {
	t.Setenv("BEAR_ENV", "prod")

	t.Run("missing file", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "absent.yaml")

		_, err := loadProductionConfig(missing)
		if err == nil {
			t.Fatal("loadProductionConfig() error = nil, want a read failure")
		}
		if !strings.Contains(err.Error(), "load production configuration") {
			t.Fatalf("error = %v, want the example's own context", err)
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("error = %v, want it to wrap os.ErrNotExist", err)
		}
	})

	t.Run("production rejects non-verifying database", func(t *testing.T) {
		path := writeProductionConfig(t, "database:\n  enabled: true\n  type: \"sqlite\"\n  dsn: \"migration.db\"\n")

		_, err := loadProductionConfig(path)
		if err == nil {
			t.Fatal("loadProductionConfig() error = nil, want the production database policy to reject SQLite")
		}
		if !strings.Contains(err.Error(), "production database type is unsupported") {
			t.Fatalf("error = %v, want the production database policy message", err)
		}
	})

	t.Run("production rejects relaxed strict loading", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "application-prod.yaml")
		relaxed := strings.Replace(productionConfigHeader, "  strict: true", "  strict: false", 1)
		if err := os.WriteFile(path, []byte(relaxed+"database:\n  enabled: false\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		_, err := loadProductionConfig(path)
		if err == nil {
			t.Fatal("loadProductionConfig() error = nil, want config.strict to be enforced in production")
		}
		if !strings.Contains(err.Error(), "config.strict") {
			t.Fatalf("error = %v, want the production strict-policy message", err)
		}
	})
}

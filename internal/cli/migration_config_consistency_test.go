package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGeneratedMigrationConfigFollowsRuntimeEnvSelection reproduces production
// issue C: with BEAR_ENV=prod the runtime selects application-prod.yaml
// (PostgreSQL) but the generator only reads the base file (MySQL).
func TestGeneratedMigrationConfigFollowsRuntimeEnvSelection(t *testing.T) {
	root := t.TempDir()
	base := "database:\n  enabled: true\n  type: \"mysql\"\n  host: \"localhost\"\n"
	prod := "database:\n  enabled: true\n  type: \"postgres\"\n  host: \"pg-internal\"\n"
	if err := os.WriteFile(filepath.Join(root, "application.yaml"), []byte(base), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "application-prod.yaml"), []byte(prod), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEAR_ENV", "prod")

	database, err := inspectGeneratedAPIDatabase(root)
	if err != nil {
		t.Fatalf("inspectGeneratedAPIDatabase failed: %v", err)
	}
	if database.Dialect != "postgres" {
		t.Fatalf("generator dialect = %q, want postgres under BEAR_ENV=prod (runtime selects application-prod.yaml)", database.Dialect)
	}
	written, err := writeGeneratedAPIMigration(root, generatedMigrationFixture(t))
	if err != nil {
		t.Fatalf("writeGeneratedAPIMigration failed: %v", err)
	}
	if len(written) == 0 {
		t.Fatal("no migration written")
	}
	up, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(written[0])))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(up), "BIGSERIAL") {
		t.Fatalf("generated DDL does not use PostgreSQL dialect under BEAR_ENV=prod:\n%s", up)
	}
	if strings.Contains(string(up), "AUTO_INCREMENT") {
		t.Fatalf("generated DDL still uses MySQL under BEAR_ENV=prod:\n%s", up)
	}
}

func TestGeneratedMigrationBaseDisabledProdEnabled(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "application.yaml"), []byte("database:\n  enabled: false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "application-prod.yaml"), []byte("database:\n  enabled: true\n  type: \"postgres\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEAR_ENV", "prod")
	t.Setenv("GIN_MODE", "")

	database, err := inspectGeneratedAPIDatabase(root)
	if err != nil {
		t.Fatal(err)
	}
	if !database.Known || !database.Enabled || database.Dialect != "postgres" {
		t.Fatalf("database = %#v, want known enabled postgres", database)
	}
	if hint := adapterHintForDatabase(root, database); hint != "" {
		t.Fatalf("hint = %q, want none when prod enables the database", hint)
	}
}

func TestGeneratedMigrationBaseEnabledProdDisabled(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "application.yaml"), []byte("database:\n  enabled: true\n  type: \"mysql\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "application-prod.yaml"), []byte("database:\n  enabled: false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEAR_ENV", "prod")
	t.Setenv("GIN_MODE", "")

	database, err := inspectGeneratedAPIDatabase(root)
	if err != nil {
		t.Fatal(err)
	}
	if !database.Known || database.Enabled {
		t.Fatalf("database = %#v, want known disabled", database)
	}
	written, err := writeGeneratedAPIMigrationWithDatabase(root, generatedMigrationFixture(t), database)
	if err != nil {
		t.Fatal(err)
	}
	if len(written) != 0 {
		t.Fatalf("written = %v, want no migration while disabled", written)
	}
	hint := adapterHintForDatabase(root, database)
	for _, want := range []string{"database.enabled", "*bear.GormAdapter", "BeansE"} {
		if !strings.Contains(hint, want) {
			t.Fatalf("hint %q does not mention %q", hint, want)
		}
	}
}

func TestGenerateResourceExplicitConfigOrderAndSubdirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/cfgtest\n\ngo 1.25.14\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "application.yaml"), []byte("database:\n  enabled: true\n  type: \"mysql\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "only-sqlite.yaml"), []byte("database:\n  enabled: true\n  type: \"sqlite\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	abs := filepath.Join(root, "only-postgres.yaml")
	if err := os.WriteFile(abs, []byte("database:\n  enabled: true\n  type: \"postgres\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEAR_ENV", "")
	t.Setenv("GIN_MODE", "")
	before, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	// Explicit relative file replaces defaults (mysql base must not win).
	result, err := generateResource(context.Background(), resourceOptions{
		Kind: "api", Name: "explicitcfg", Directory: root, ConfigPaths: []string{"only-sqlite.yaml"},
	})
	if err != nil {
		t.Fatalf("explicit config generation failed: %v", err)
	}
	if result.Path != filepath.Join("internal", "explicitcfg") {
		t.Fatalf("path = %q", result.Path)
	}
	up, err := os.ReadFile(filepath.Join(root, "migrations", "001_create_explicitcfg.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(up), "AUTOINCREMENT") {
		t.Fatalf("explicit sqlite DDL missing AUTOINCREMENT:\n%s", up)
	}

	// Absolute path plus ordering: last file wins.
	result, err = generateResource(context.Background(), resourceOptions{
		Kind: "api", Name: "absolutecfg", Directory: root, ConfigPaths: []string{"only-sqlite.yaml", abs},
	})
	if err != nil {
		t.Fatalf("absolute config generation failed: %v", err)
	}
	_ = result
	up, err = os.ReadFile(filepath.Join(root, "migrations", "002_create_absolutecfg.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(up), "BIGSERIAL") {
		t.Fatalf("ordered absolute DDL missing BIGSERIAL:\n%s", up)
	}
	if after, err := os.Getwd(); err != nil || after != before {
		t.Fatalf("working directory changed: before=%q after=%q err=%v", before, after, err)
	}
}

func TestGenerateResourceConfigFlagRejectedForModelAndDTO(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/cfgreject\n\ngo 1.25.14\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"model", "dto"} {
		if _, err := generateResource(context.Background(), resourceOptions{
			Kind: kind, Name: "cfg-" + kind, Directory: root, ConfigPaths: []string{"only-sqlite.yaml"},
		}); err == nil || !strings.Contains(err.Error(), "--config") {
			t.Fatalf("kind %s error = %v, want --config usage error", kind, err)
		}
	}
	var stdout, stderr bytes.Buffer
	if code := Execute([]string{"gen", "dto", "cfgdto", "--config", "only-sqlite.yaml"}, &stdout, &stderr); code == 0 {
		t.Fatal("gen dto --config exited 0, want usage failure")
	}
}

func TestGenerateResourceBadConfigWritesNothing(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/cfgfail\n\ngo 1.25.14\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "application.yaml"), []byte("database:\n  enabled: true\n  type: \"sqlite\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEAR_ENV", "")
	t.Setenv("GIN_MODE", "")
	before := snapshotTree(t, root)
	for _, paths := range [][]string{{"missing.yaml"}, {"broken.yaml"}} {
		if len(paths) == 1 && paths[0] == "broken.yaml" {
			if err := os.WriteFile(filepath.Join(root, "broken.yaml"), []byte("database: [unclosed\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := generateResource(context.Background(), resourceOptions{
			Kind: "api", Name: "shouldnotexist", Directory: root, ConfigPaths: paths,
		}); err == nil {
			t.Fatalf("config %v was accepted", paths)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "unknown.yaml"), []byte("database:\n  enabled: true\n  typo_field: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := generateResource(context.Background(), resourceOptions{
		Kind: "api", Name: "shouldnotexist", Directory: root, ConfigPaths: []string{"unknown.yaml"},
	}); err == nil || !strings.Contains(err.Error(), "typo_field") {
		t.Fatalf("unknown-field error = %v", err)
	}
	after := snapshotTree(t, root)
	// Only the intentionally written broken/unknown fixtures may differ; no
	// resource, migration, manifest or go.mod may appear.
	delete(after, "broken.yaml")
	delete(after, "unknown.yaml")
	delete(before, "broken.yaml")
	delete(before, "unknown.yaml")
	if len(before) != len(after) {
		t.Fatalf("tree changed on config failure: before=%v after=%v", before, after)
	}
	for name, hash := range before {
		if after[name] != hash {
			t.Fatalf("file %s changed on config failure", name)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "internal", "shouldnotexist")); !os.IsNotExist(err) {
		t.Fatal("failed generation left a resource package behind")
	}
}

func TestGenerateResourceVersionsAdvanceAndPreserveHistory(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/cfgversions\n\ngo 1.25.14\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "application.yaml"), []byte("database:\n  enabled: true\n  type: \"sqlite\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEAR_ENV", "")
	t.Setenv("GIN_MODE", "")
	if _, err := generateResource(context.Background(), resourceOptions{Kind: "api", Name: "first", Directory: root}); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(filepath.Join(root, "migrations", "001_create_first.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	firstHash := sha256.Sum256(first)
	if _, err := generateResource(context.Background(), resourceOptions{Kind: "api", Name: "second", Directory: root}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "migrations", "002_create_second.up.sql")); err != nil {
		t.Fatalf("second migration missing: %v", err)
	}
	again, err := os.ReadFile(filepath.Join(root, "migrations", "001_create_first.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(again) != firstHash {
		t.Fatal("first migration was rewritten by a later generation")
	}
}

func snapshotTree(t *testing.T, root string) map[string][32]byte {
	t.Helper()
	out := map[string][32]byte{}
	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		out[rel] = sha256.Sum256(data)
		return nil
	})
	return out
}

package bear

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeGenerationConfig(t *testing.T, dir, name, contents string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestDatabaseConfigForGenerationFollowsProdOverlay is the helper-level
// contract for production issue C: under BEAR_ENV=prod the generation
// selection must agree with the runtime file chain.
func TestDatabaseConfigForGenerationFollowsProdOverlay(t *testing.T) {
	dir := t.TempDir()
	writeGenerationConfig(t, dir, "application.yaml", "database:\n  enabled: true\n  type: \"mysql\"\n")
	writeGenerationConfig(t, dir, "application-prod.yaml", "database:\n  enabled: true\n  type: \"postgres\"\n")
	t.Setenv("BEAR_ENV", "prod")
	t.Setenv("GIN_MODE", "")

	snapshot, err := LoadDatabaseConfigForGeneration(dir)
	if err != nil {
		t.Fatalf("LoadDatabaseConfigForGeneration failed: %v", err)
	}
	if snapshot == nil || !snapshot.Enabled || !strings.EqualFold(snapshot.Type, "postgres") {
		t.Fatalf("snapshot = %#v, want enabled postgres", snapshot)
	}
}

func TestDatabaseConfigForGenerationBaseDisabledProdEnabled(t *testing.T) {
	dir := t.TempDir()
	writeGenerationConfig(t, dir, "application.yaml", "database:\n  enabled: false\n")
	writeGenerationConfig(t, dir, "application-prod.yaml", "database:\n  enabled: true\n  type: \"postgres\"\n")
	t.Setenv("BEAR_ENV", "prod")
	t.Setenv("GIN_MODE", "")

	snapshot, err := LoadDatabaseConfigForGeneration(dir)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot == nil || !snapshot.Enabled {
		t.Fatalf("snapshot = %#v, want prod enabled database", snapshot)
	}
}

func TestDatabaseConfigForGenerationBaseEnabledProdDisabled(t *testing.T) {
	dir := t.TempDir()
	writeGenerationConfig(t, dir, "application.yaml", "database:\n  enabled: true\n  type: \"mysql\"\n")
	writeGenerationConfig(t, dir, "application-prod.yaml", "database:\n  enabled: false\n")
	t.Setenv("BEAR_ENV", "prod")
	t.Setenv("GIN_MODE", "")

	snapshot, err := LoadDatabaseConfigForGeneration(dir)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot == nil || snapshot.Enabled {
		t.Fatalf("snapshot = %#v, want prod disabled database", snapshot)
	}
}

func TestDatabaseConfigForGenerationGinModeReleaseSelectsProd(t *testing.T) {
	dir := t.TempDir()
	writeGenerationConfig(t, dir, "application.yaml", "database:\n  enabled: true\n  type: \"mysql\"\n")
	writeGenerationConfig(t, dir, "application-prod.yaml", "database:\n  enabled: true\n  type: \"postgres\"\n")
	t.Setenv("BEAR_ENV", "")
	t.Setenv("GIN_MODE", "release")

	snapshot, err := LoadDatabaseConfigForGeneration(dir)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot == nil || !strings.EqualFold(snapshot.Type, "postgres") {
		t.Fatalf("snapshot = %#v, want postgres via GIN_MODE=release", snapshot)
	}
}

func TestDatabaseConfigForGenerationCompatibilityFileAndJSONPriority(t *testing.T) {
	dir := t.TempDir()
	writeGenerationConfig(t, dir, "application.yaml", "database:\n  enabled: true\n  type: \"mysql\"\n")
	// Raw-environment compatibility: BEAR_ENV=production normalizes to the
	// same overlay LoadConfig uses; config.json decodes last and wins.
	writeGenerationConfig(t, dir, "application-production.yaml", "database:\n  enabled: true\n  type: \"postgres\"\n")
	writeGenerationConfig(t, dir, "config.json", `{"database": {"enabled": true, "type": "sqlite"}}`)
	t.Setenv("BEAR_ENV", "production")
	t.Setenv("GIN_MODE", "")

	snapshot, err := LoadDatabaseConfigForGeneration(dir)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot == nil || !strings.EqualFold(snapshot.Type, "sqlite") {
		t.Fatalf("snapshot = %#v, want config.json sqlite to win", snapshot)
	}
}

func TestDatabaseConfigForGenerationExplicitPathsReplaceDefaults(t *testing.T) {
	dir := t.TempDir()
	writeGenerationConfig(t, dir, "application.yaml", "database:\n  enabled: true\n  type: \"mysql\"\n")
	writeGenerationConfig(t, dir, "application-prod.yaml", "database:\n  enabled: true\n  type: \"postgres\"\n")
	writeGenerationConfig(t, dir, "only-sqlite.yaml", "database:\n  enabled: true\n  type: \"sqlite\"\n")
	t.Setenv("BEAR_ENV", "prod")
	t.Setenv("GIN_MODE", "")

	// Only the explicit file applies; the prod overlay must not sneak back in.
	snapshot, err := LoadDatabaseConfigForGeneration(dir, "only-sqlite.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot == nil || !strings.EqualFold(snapshot.Type, "sqlite") {
		t.Fatalf("explicit snapshot = %#v, want sqlite", snapshot)
	}

	// Later files win, in order; absolute paths work as-is.
	second := writeGenerationConfig(t, dir, "second.yaml", "database:\n  enabled: true\n  type: \"mysql\"\n")
	snapshot, err = LoadDatabaseConfigForGeneration(dir, "only-sqlite.yaml", second)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot == nil || !strings.EqualFold(snapshot.Type, "mysql") {
		t.Fatalf("ordered snapshot = %#v, want second file mysql", snapshot)
	}
}

func TestDatabaseConfigForGenerationResolvesSubdirectoryWithoutChdir(t *testing.T) {
	dir := t.TempDir()
	writeGenerationConfig(t, dir, "application.yaml", "database:\n  enabled: true\n  type: \"postgres\"\n")
	sub := filepath.Join(dir, "sub", "nested")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	before, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEAR_ENV", "")
	t.Setenv("GIN_MODE", "")

	snapshot, err := LoadDatabaseConfigForGeneration(dir)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot == nil || !strings.EqualFold(snapshot.Type, "postgres") {
		t.Fatalf("snapshot = %#v, want postgres from project root", snapshot)
	}
	_ = sub
	if after, err := os.Getwd(); err != nil || after != before {
		t.Fatalf("working directory changed: before=%q after=%q err=%v", before, after, err)
	}
}

func TestDatabaseConfigForGenerationNoConfigPreservesLegacyBehaviour(t *testing.T) {
	t.Setenv("BEAR_ENV", "")
	t.Setenv("GIN_MODE", "")
	snapshot, err := LoadDatabaseConfigForGeneration(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot != nil {
		t.Fatalf("snapshot = %#v, want nil for config-free projects", snapshot)
	}
}

func TestDatabaseConfigForGenerationExplicitErrors(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BEAR_ENV", "")
	t.Setenv("GIN_MODE", "")
	if _, err := LoadDatabaseConfigForGeneration(dir, "missing.yaml"); err == nil {
		t.Fatal("missing explicit config was accepted")
	}
	writeGenerationConfig(t, dir, "broken.yaml", "database: [unclosed\n")
	if _, err := LoadDatabaseConfigForGeneration(dir, "broken.yaml"); err == nil {
		t.Fatal("broken explicit config was accepted")
	}
	writeGenerationConfig(t, dir, "unknown.yaml", "database:\n  enabled: true\n  typo_field: true\n")
	if _, err := LoadDatabaseConfigForGeneration(dir, "unknown.yaml"); err == nil || !strings.Contains(err.Error(), "typo_field") {
		t.Fatalf("unknown-field error = %v, want typo_field", err)
	}
}

func TestDatabaseConfigForGenerationSkipsRuntimeSecrets(t *testing.T) {
	dir := t.TempDir()
	writeGenerationConfig(t, dir, "application.yaml", "database:\n  enabled: true\n  type: \"postgres\"\nauth:\n  enabled: true\n")
	t.Setenv("BEAR_ENV", "prod")
	t.Setenv("GIN_MODE", "")
	t.Setenv("BEAR_AUTH_JWT_SECRET", "")
	t.Setenv("JWT_SECRET", "")

	snapshot, err := LoadDatabaseConfigForGeneration(dir)
	if err != nil {
		t.Fatalf("generation selection should not require JWT secrets: %v", err)
	}
	if snapshot == nil || !strings.EqualFold(snapshot.Type, "postgres") {
		t.Fatalf("snapshot = %#v, want postgres", snapshot)
	}
	if _, err := LoadConfig(filepath.Join(dir, "application.yaml")); err == nil {
		t.Fatal("LoadConfig accepted a production config without JWT credentials")
	}
}

func TestDatabaseConfigForGenerationSurfacesEnvOverrideErrors(t *testing.T) {
	dir := t.TempDir()
	writeGenerationConfig(t, dir, "application.yaml", "database:\n  enabled: true\n  type: \"mysql\"\n")
	t.Setenv("BEAR_ENV", "")
	t.Setenv("GIN_MODE", "")
	t.Setenv("BEAR_SERVER_PORT", "not-a-port")
	defer func() { _ = os.Unsetenv("BEAR_SERVER_PORT") }()

	if _, err := LoadDatabaseConfigForGeneration(dir); err == nil {
		t.Fatal("invalid BEAR_SERVER_PORT was silently ignored")
	}
}

func TestDatabaseConfigForGenerationMissingDatabaseBlockStaysDisabled(t *testing.T) {
	dir := t.TempDir()
	writeGenerationConfig(t, dir, "application.yaml", "server:\n  port: 8080\n")
	t.Setenv("BEAR_ENV", "")
	t.Setenv("GIN_MODE", "")

	snapshot, err := LoadDatabaseConfigForGeneration(dir)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot == nil || snapshot.Enabled {
		t.Fatalf("snapshot = %#v, want default disabled database", snapshot)
	}
}

func TestDatabaseConfigForGenerationAgreesWithLoadConfig(t *testing.T) {
	dir := t.TempDir()
	writeGenerationConfig(t, dir, "application.yaml", "database:\n  enabled: true\n  type: \"mysql\"\n  host: \"db\"\n  user: \"u\"\n  password: \"p\"\n  dbname: \"app\"\n")
	writeGenerationConfig(t, dir, "application-dev.yaml", "database:\n  host: \"dev-db\"\n")
	t.Setenv("BEAR_ENV", "dev")
	t.Setenv("GIN_MODE", "")

	snapshot, err := LoadDatabaseConfigForGeneration(dir)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := LoadConfig(
		filepath.Join(dir, "application.yaml"),
		filepath.Join(dir, "application-dev.yaml"),
	)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	if snapshot == nil || runtime.DB == nil {
		t.Fatalf("snapshot=%#v runtime=%#v", snapshot, runtime.DB)
	}
	if snapshot.Host != runtime.DB.Host || snapshot.Type != runtime.DB.Type || snapshot.Enabled != runtime.DB.Enabled {
		t.Fatalf("generation snapshot %#v disagrees with runtime %#v", snapshot, runtime.DB)
	}
}

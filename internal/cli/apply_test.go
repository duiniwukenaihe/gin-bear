package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/duiniwukenaihe/gin-bear/internal/scaffold"
)

func writePlanFile(t *testing.T, dir string, document previewDocument) string {
	t.Helper()
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "plan.json")
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func previewDocumentForApply(t *testing.T, dir string, args ...string) previewDocument {
	t.Helper()
	t.Setenv("BEAR_ENV", "")
	t.Setenv("GIN_MODE", "")
	stdout, _, code := executeIn(t, dir, args...)
	if code != 0 {
		t.Fatalf("preview exit = %d\n%s", code, stdout)
	}
	return decodePreview(t, stdout)
}

// TestApplyExecutesPreviewedPlanAndUpgradesManifest is the W4 apply
// acceptance: a fresh preview applies byte-identically, moves the manifest to
// v2 with digests, and cannot be applied twice.
func TestApplyExecutesPreviewedPlanAndUpgradesManifest(t *testing.T) {
	project := previewProject(t, "database:\n  enabled: true\n  type: \"sqlite\"\n")
	if err := scaffold.WriteManifest(project, scaffold.NewManifest("example.com/preview", "v0.0.0")); err != nil {
		t.Fatal(err)
	}
	document := previewDocumentForApply(t, project, "gen", "api", "invoice", "--fields", "name:string", "--dry-run", "--format", "json")
	planPath := writePlanFile(t, project, document)

	stdout, _, code := executeIn(t, project, "gen", "apply", "--plan", planPath)
	if code != 0 {
		t.Fatalf("apply exit = %d\n%s", code, stdout)
	}
	if !strings.Contains(stdout, "Applied internal/invoice") {
		t.Fatalf("apply stdout = %q", stdout)
	}
	for _, file := range document.Plan.Files {
		contents, err := os.ReadFile(filepath.Join(project, filepath.FromSlash(file.Rel)))
		if err != nil {
			t.Fatalf("applied file %s missing: %v", file.Rel, err)
		}
		if digestOf(contents) != file.SHA256 {
			t.Fatalf("applied file %s differs from the plan", file.Rel)
		}
	}
	manifest, err := scaffold.ReadManifest(project)
	if err != nil {
		t.Fatalf("read manifest after apply: %v", err)
	}
	if manifest.TemplateVersion != scaffold.TemplateVersionFiles {
		t.Fatalf("manifest version = %d, want v2 after apply", manifest.TemplateVersion)
	}
	var entry *scaffold.GeneratedAPI
	for i := range manifest.APIs {
		if manifest.APIs[i].Package == "invoice" {
			entry = &manifest.APIs[i]
		}
	}
	if entry == nil {
		t.Fatalf("manifest missing invoice entry: %+v", manifest.APIs)
	}
	if len(entry.Files) != len(document.Plan.Files) {
		t.Fatalf("manifest digests = %d, want %d", len(entry.Files), len(document.Plan.Files))
	}

	if _, _, code := executeIn(t, project, "gen", "apply", "--plan", planPath); code == 0 {
		t.Fatal("second apply exited 0, want rejection of an already-applied plan")
	}
}

// TestApplyReplaysAbsoluteConfigInsideProject is the R7 contract
// acceptance: an absolute --config inside the project previews to a
// project-relative input and applies; an absolute --config outside the
// project fails at preview time instead of producing an unappliable plan.
func TestApplyReplaysAbsoluteConfigInsideProject(t *testing.T) {
	project := previewProject(t, "")
	configName := "explicit-pg.yaml"
	if err := os.WriteFile(filepath.Join(project, configName), []byte("database:\n  enabled: true\n  type: \"postgres\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	absolute, err := filepath.Abs(filepath.Join(project, configName))
	if err != nil {
		t.Fatal(err)
	}
	document := previewDocumentForApply(t, project, "gen", "api", "invoice", "--config", absolute, "--dry-run", "--format", "json")
	if len(document.Plan.ConfigInputs) != 1 || document.Plan.ConfigInputs[0] != configName {
		t.Fatalf("plan config_inputs = %v, want [%s]", document.Plan.ConfigInputs, configName)
	}
	if len(document.Plan.Config) != 1 || document.Plan.Config[0] != absolute {
		t.Fatalf("plan config_sources = %v, want [%s]", document.Plan.Config, absolute)
	}
	planPath := writePlanFile(t, project, document)
	stdout, _, code := executeIn(t, project, "gen", "apply", "--plan", planPath)
	if code != 0 {
		t.Fatalf("apply exit = %d\n%s", code, stdout)
	}
	if !strings.Contains(stdout, "Applied internal/invoice") {
		t.Fatalf("apply stdout = %q", stdout)
	}

	outside := filepath.Join(t.TempDir(), "outside.yaml")
	if err := os.WriteFile(outside, []byte("database:\n  enabled: true\n  type: \"sqlite\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := executeIn(t, project, "gen", "api", "dealer", "--config", outside, "--dry-run", "--format", "json")
	if code == 0 {
		t.Fatal("outside-project absolute --config preview exited 0, want refusal")
	}
	if !strings.Contains(stderr, "outside the project") {
		t.Fatalf("preview stderr = %q, want outside-project refusal", stderr)
	}
}

// TestApplyRejectsStalePlan proves external modifications invalidate a plan:
// a new migration version, a changed dialect, and a pre-created target.
func TestApplyRejectsStalePlan(t *testing.T) {
	t.Run("migration version moved", func(t *testing.T) {
		project := previewProject(t, "database:\n  enabled: true\n  type: \"sqlite\"\n")
		document := previewDocumentForApply(t, project, "gen", "api", "invoice", "--dry-run", "--format", "json")
		planPath := writePlanFile(t, project, document)
		if err := os.MkdirAll(filepath.Join(project, "migrations"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(project, "migrations", "001_create_other.up.sql"), []byte("SELECT 1;\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, stderr, code := executeIn(t, project, "gen", "apply", "--plan", planPath)
		if code == 0 {
			t.Fatal("stale apply exited 0")
		}
		if !strings.Contains(stderr, "stale") {
			t.Fatalf("stale apply error missing stale marker: %q", stderr)
		}
	})

	t.Run("dialect changed", func(t *testing.T) {
		project := previewProject(t, "database:\n  enabled: true\n  type: \"sqlite\"\n")
		document := previewDocumentForApply(t, project, "gen", "api", "invoice", "--dry-run", "--format", "json")
		planPath := writePlanFile(t, project, document)
		if err := os.WriteFile(filepath.Join(project, "application.yaml"), []byte("database:\n  enabled: true\n  type: \"postgres\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, _, code := executeIn(t, project, "gen", "apply", "--plan", planPath)
		if code == 0 {
			t.Fatal("stale apply after dialect change exited 0")
		}
	})

	t.Run("target pre-created", func(t *testing.T) {
		project := previewProject(t, "database:\n  enabled: false\n")
		document := previewDocumentForApply(t, project, "gen", "api", "invoice", "--dry-run", "--format", "json")
		planPath := writePlanFile(t, project, document)
		if err := os.MkdirAll(filepath.Join(project, "internal", "invoice"), 0o755); err != nil {
			t.Fatal(err)
		}
		_, _, code := executeIn(t, project, "gen", "apply", "--plan", planPath)
		if code == 0 {
			t.Fatal("stale apply with pre-created target exited 0")
		}
	})
}

// TestApplyRejectsUntrustedPaths proves plan files cannot smuggle absolute,
// dot-dot, or symlink-escaping write targets.
func TestApplyRejectsUntrustedPaths(t *testing.T) {
	t.Run("dot-dot", func(t *testing.T) {
		project := previewProject(t, "database:\n  enabled: false\n")
		document := previewDocumentForApply(t, project, "gen", "api", "invoice", "--dry-run", "--format", "json")
		document.Plan.Files[0].Rel = "../evil.go"
		planPath := writePlanFile(t, project, document)
		_, stderr, code := executeIn(t, project, "gen", "apply", "--plan", planPath)
		if code == 0 {
			t.Fatal("traversal apply exited 0")
		}
		if !strings.Contains(stderr, "escapes") {
			t.Fatalf("traversal error missing escape marker: %q", stderr)
		}
		if _, err := os.Stat(filepath.Join(project, "evil.go")); !os.IsNotExist(err) {
			t.Fatal("traversal apply wrote outside the project")
		}
	})

	t.Run("absolute", func(t *testing.T) {
		project := previewProject(t, "database:\n  enabled: false\n")
		document := previewDocumentForApply(t, project, "gen", "api", "invoice", "--dry-run", "--format", "json")
		document.Plan.Files[0].Rel = "/tmp/evil.go"
		planPath := writePlanFile(t, project, document)
		_, _, code := executeIn(t, project, "gen", "apply", "--plan", planPath)
		if code == 0 {
			t.Fatal("absolute-path apply exited 0")
		}
	})

	t.Run("symlink escape", func(t *testing.T) {
		project := previewProject(t, "database:\n  enabled: false\n")
		outside := t.TempDir()
		if err := os.MkdirAll(filepath.Join(project, "internal"), 0o755); err != nil {
			t.Fatal(err)
		}
		// Point internal/ outside the project, then apply a valid plan.
		if err := os.Remove(filepath.Join(project, "internal")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(project, "internal")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		document := previewDocumentForApply(t, project, "gen", "api", "invoice", "--dry-run", "--format", "json")
		planPath := writePlanFile(t, project, document)
		_, stderr, code := executeIn(t, project, "gen", "apply", "--plan", planPath)
		if code == 0 {
			t.Fatal("symlink-escape apply exited 0")
		}
		if !strings.Contains(stderr, "symlink") {
			t.Fatalf("symlink error missing marker: %q", stderr)
		}
		entries, err := os.ReadDir(outside)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Fatalf("apply escaped through symlink: %v", entries)
		}
	})
}

// TestApplyRequiresPlanFile pins the apply CLI contract.
func TestApplyRequiresPlanFile(t *testing.T) {
	project := previewProject(t, "")
	_, _, code := executeIn(t, project, "gen", "apply")
	if code == 0 {
		t.Fatal("apply without --plan exited 0")
	}
	_, _, code = executeIn(t, project, "gen", "apply", "--plan", filepath.Join(project, "missing.json"))
	if code == 0 {
		t.Fatal("apply with missing plan exited 0")
	}
	_, _, code = executeIn(t, project, "gen", "apply", "--plan", "x", "--fields", "a:string")
	if code == 0 {
		t.Fatal("apply with --fields exited 0, want combo rejection")
	}
	if _, err := os.Stat(filepath.Join(project, "internal")); !os.IsNotExist(err) {
		t.Fatal("rejected apply wrote files")
	}
}

func digestOf(contents []byte) string {
	sum := sha256.Sum256(contents)
	return hex.EncodeToString(sum[:])
}

// TestApplyReplaysExplicitConfig is the R7 acceptance: a plan previewed
// against explicit config applies with the same selection, even when the
// default chain would resolve differently.
func TestApplyReplaysExplicitConfig(t *testing.T) {
	project := previewProject(t, "database:\n  enabled: true\n  type: \"mysql\"\n")
	if err := os.WriteFile(filepath.Join(project, "application-prod.yaml"), []byte("database:\n  enabled: true\n  type: \"postgres\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := scaffold.WriteManifest(project, scaffold.NewManifest("example.com/preview", "v0.0.0")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEAR_ENV", "prod")
	t.Setenv("GIN_MODE", "")

	t.Chdir(project)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := Execute([]string{"gen", "api", "invoice", "--config", "application-prod.yaml", "--dry-run", "--format", "json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("preview exit = %d\n%s%s", code, stdout.String(), stderr.String())
	}
	document := decodePreview(t, stdout.String())
	if len(document.Plan.ConfigInputs) != 1 || document.Plan.ConfigInputs[0] != "application-prod.yaml" {
		t.Fatalf("plan config_inputs = %v", document.Plan.ConfigInputs)
	}
	if document.Plan.Migration == nil || !strings.Contains(document.Plan.Migration.Up, "BIGSERIAL") {
		t.Fatalf("plan migration = %+v, want postgres DDL", document.Plan.Migration)
	}
	planPath := writePlanFile(t, project, document)

	stdout.Reset()
	stderr.Reset()
	if code := Execute([]string{"gen", "apply", "--plan", planPath}, &stdout, &stderr); code != 0 {
		t.Fatalf("apply exit = %d\n%s%s", code, stdout.String(), stderr.String())
	}
	contents, err := os.ReadFile(filepath.Join(project, "migrations", "001_create_invoice.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), "BIGSERIAL") {
		t.Fatalf("applied migration is not postgres:\n%s", contents)
	}
}

// TestApplyRejectsMissingExplicitConfig proves a removed explicit file
// invalidates the plan instead of silently falling back to defaults.
func TestApplyRejectsMissingExplicitConfig(t *testing.T) {
	project := previewProject(t, "database:\n  enabled: true\n  type: \"sqlite\"\n")
	if err := os.WriteFile(filepath.Join(project, "only.yaml"), []byte("database:\n  enabled: true\n  type: \"sqlite\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEAR_ENV", "")
	t.Setenv("GIN_MODE", "")
	t.Chdir(project)
	var stdout bytes.Buffer
	if code := Execute([]string{"gen", "api", "invoice", "--config", "only.yaml", "--dry-run", "--format", "json"}, &stdout, &bytes.Buffer{}); code != 0 {
		t.Fatalf("preview exit = %d", code)
	}
	document := decodePreview(t, stdout.String())
	planPath := writePlanFile(t, project, document)
	if err := os.Remove(filepath.Join(project, "only.yaml")); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	if code := Execute([]string{"gen", "apply", "--plan", planPath}, &stdout, &stderr); code == 0 {
		t.Fatal("apply with removed explicit config exited 0")
	}
}

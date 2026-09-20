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

func previewProject(t *testing.T, config string) string {
	t.Helper()
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "go.mod"), []byte("module example.com/preview\n\ngo 1.25.14\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if config != "" {
		if err := os.WriteFile(filepath.Join(project, "application.yaml"), []byte(config), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return project
}

func executeIn(t *testing.T, dir string, args ...string) (string, string, int) {
	t.Helper()
	t.Chdir(dir)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Execute(args, &stdout, &stderr)
	return stdout.String(), stderr.String(), code
}

func decodePreview(t *testing.T, stdout string) previewDocument {
	t.Helper()
	var document previewDocument
	decoder := json.NewDecoder(strings.NewReader(stdout))
	if err := decoder.Decode(&document); err != nil {
		t.Fatalf("decode preview JSON: %v\n%s", err, stdout)
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		t.Fatalf("preview stdout holds more than one document:\n%s", stdout)
	}
	return document
}

func shaFile(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(contents)
	return hex.EncodeToString(sum[:])
}

// TestPreviewMatchesActualGeneration is the W4 consistency acceptance: the
// preview observes exactly the bytes real generation publishes.
func TestPreviewMatchesActualGeneration(t *testing.T) {
	project := previewProject(t, "database:\n  enabled: true\n  type: \"sqlite\"\n  dsn: \"preview.db\"\n")
	if err := scaffold.WriteManifest(project, scaffold.NewManifest("example.com/preview", "v0.0.0")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEAR_ENV", "")
	t.Setenv("GIN_MODE", "")

	stdout, _, code := executeIn(t, project, "gen", "api", "invoice", "--fields", "name:string", "--dry-run", "--format", "json")
	if code != 0 {
		t.Fatalf("preview exit = %d\n%s", code, stdout)
	}
	document := decodePreview(t, stdout)
	if !document.DryRun || document.SchemaVersion != previewSchemaVersion {
		t.Fatalf("preview envelope = %+v", document)
	}
	plan := document.Plan
	if plan.Target != "internal/invoice" || len(plan.Files) == 0 {
		t.Fatalf("preview plan target/files = %q/%d", plan.Target, len(plan.Files))
	}
	if plan.Migration == nil || !strings.Contains(plan.Migration.Up, "AUTOINCREMENT") {
		t.Fatalf("preview migration = %+v, want sqlite DDL", plan.Migration)
	}
	if plan.Manifest == nil || plan.Manifest.Added.Package != "invoice" {
		t.Fatalf("preview manifest = %+v, want invoice entry", plan.Manifest)
	}

	stdout, _, code = executeIn(t, project, "gen", "api", "invoice", "--fields", "name:string")
	if code != 0 {
		t.Fatalf("real generation exit = %d\n%s", code, stdout)
	}
	for _, file := range plan.Files {
		if got := shaFile(t, filepath.Join(project, filepath.FromSlash(file.Rel))); got != file.SHA256 {
			t.Fatalf("file %s sha = %s, preview said %s", file.Rel, got, file.SHA256)
		}
	}
	for _, relative := range plan.Migration.Files {
		path := filepath.Join(project, filepath.FromSlash(relative))
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("migration %s missing: %v", relative, err)
		}
		want := plan.Migration.Up
		if strings.HasSuffix(relative, ".down.sql") {
			want = plan.Migration.Down
		}
		if string(contents) != want {
			t.Fatalf("migration %s differs from preview", relative)
		}
	}
	manifest, err := scaffold.ReadManifest(project)
	if err != nil {
		t.Fatalf("read manifest after generation: %v", err)
	}
	found := false
	for _, api := range manifest.APIs {
		if api.Package == "invoice" {
			found = true
		}
	}
	if !found {
		t.Fatalf("manifest missing invoice entry: %+v", manifest.APIs)
	}
}

// TestPreviewIsStable requires byte-identical previews across runs.
func TestPreviewIsStable(t *testing.T) {
	project := previewProject(t, "database:\n  enabled: true\n  type: \"sqlite\"\n")
	t.Setenv("BEAR_ENV", "")
	t.Setenv("GIN_MODE", "")
	first, _, code := executeIn(t, project, "gen", "api", "invoice", "--dry-run", "--format", "json")
	if code != 0 {
		t.Fatalf("first preview exit = %d", code)
	}
	second, _, code := executeIn(t, project, "gen", "api", "invoice", "--dry-run", "--format", "json")
	if code != 0 {
		t.Fatalf("second preview exit = %d", code)
	}
	if first != second {
		t.Fatal("preview output differs between runs")
	}
}

// TestPreviewWritesNothing proves preview creates no directories, lock files,
// manifests, migrations, or go.mod changes.
func TestPreviewWritesNothing(t *testing.T) {
	project := previewProject(t, "database:\n  enabled: true\n  type: \"sqlite\"\n")
	beforeGoMod := shaFile(t, filepath.Join(project, "go.mod"))
	t.Setenv("BEAR_ENV", "")
	t.Setenv("GIN_MODE", "")
	stdout, _, code := executeIn(t, project, "gen", "api", "invoice", "--dry-run", "--format", "json")
	if code != 0 {
		t.Fatalf("preview exit = %d\n%s", code, stdout)
	}
	for _, unexpected := range []string{"internal", "migrations", ".bear"} {
		if _, err := os.Stat(filepath.Join(project, unexpected)); !os.IsNotExist(err) {
			t.Fatalf("preview created %s", unexpected)
		}
	}
	for _, lock := range []string{".bear/generate.lock", "generate.lock"} {
		if _, err := os.Stat(filepath.Join(project, filepath.FromSlash(lock))); !os.IsNotExist(err) {
			t.Fatalf("preview created lock %s", lock)
		}
	}
	if got := shaFile(t, filepath.Join(project, "go.mod")); got != beforeGoMod {
		t.Fatal("preview modified go.mod")
	}
}

// TestPreviewListsConflicts keeps preview advisory while real generation
// still refuses: conflicts are data in preview, errors in generation.
func TestPreviewListsConflicts(t *testing.T) {
	project := previewProject(t, "database:\n  enabled: true\n  type: \"sqlite\"\n")
	if err := os.MkdirAll(filepath.Join(project, "internal", "invoice"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEAR_ENV", "")
	t.Setenv("GIN_MODE", "")
	stdout, _, code := executeIn(t, project, "gen", "api", "invoice", "--dry-run", "--format", "json")
	if code != 0 {
		t.Fatalf("conflicted preview exit = %d, want advisory 0\n%s", code, stdout)
	}
	document := decodePreview(t, stdout)
	if len(document.Plan.Conflicts) == 0 {
		t.Fatal("conflicted preview lists no conflicts")
	}
	_, _, code = executeIn(t, project, "gen", "api", "invoice")
	if code == 0 {
		t.Fatal("conflicted generation exited 0, want refusal")
	}
}

// TestPreviewFormatRequiresDryRun rejects --format on real runs so the
// machine-readable stdout contract stays unambiguous.
func TestPreviewFormatRequiresDryRun(t *testing.T) {
	project := previewProject(t, "")
	t.Setenv("BEAR_ENV", "")
	t.Setenv("GIN_MODE", "")
	_, _, code := executeIn(t, project, "gen", "dto", "invoice", "--format", "json")
	if code == 0 {
		t.Fatal("real generation with --format exited 0, want usage error")
	}
	if _, err := os.Stat(filepath.Join(project, "internal")); !os.IsNotExist(err) {
		t.Fatal("rejected generation wrote files")
	}
}

// TestPreviewModelDTOCoversNonAPIGeneration ensures preview works where no
// database, migration, or manifest applies.
func TestPreviewModelDTOCoversNonAPIGeneration(t *testing.T) {
	project := previewProject(t, "")
	t.Setenv("BEAR_ENV", "")
	t.Setenv("GIN_MODE", "")
	stdout, _, code := executeIn(t, project, "gen", "model", "invoice", "--dry-run", "--format", "json")
	if code != 0 {
		t.Fatalf("model preview exit = %d\n%s", code, stdout)
	}
	document := decodePreview(t, stdout)
	if len(document.Plan.Files) != 1 || !strings.HasSuffix(document.Plan.Files[0].Rel, "model.go") {
		t.Fatalf("model preview files = %+v", document.Plan.Files)
	}
	if document.Plan.Migration != nil || document.Plan.Manifest != nil {
		t.Fatal("model preview must not plan migrations or manifest updates")
	}
}

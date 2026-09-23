package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/duiniwukenaihe/gin-bear/internal/scaffold"
)

func jsonMarshal(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func basePlan() *resourcePlan {
	return &resourcePlan{
		Kind: "api", Name: "invoice", Fields: "name:string",
		ProjectRoot: "/proj", Config: []string{"application.yaml"},
		Framework: "v0.0.0", Template: 1, Target: "internal/invoice",
		Files:     []plannedFile{{Rel: "internal/invoice/model.go", Action: "create", Bytes: 10, SHA256: "abc"}},
		Migration: &plannedMigration{Version: "001", Name: "create_invoice", Dialect: "sqlite", Files: []string{"migrations/001_create_invoice.up.sql"}, Up: "up", Down: "down"},
		Manifest:  &plannedManifest{Path: ".bear/scaffold.json", Action: "update"},
		GoMod:     []modRequire{{Path: "gorm.io/gorm", Version: "v1.26.0"}},
	}
}

func TestPlansEqualAcceptsIdentical(t *testing.T) {
	if diff := plansEqual(basePlan(), basePlan()); diff != "" {
		t.Fatalf("identical plans differ: %s", diff)
	}
}

func TestPlansEqualNamesEveryDifference(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*resourcePlan)
		want   string
	}{
		{"inputs", func(p *resourcePlan) { p.Fields = "name:string,age:int" }, "inputs changed"},
		{"toolchain", func(p *resourcePlan) { p.Framework = "v9.9.9" }, "target or toolchain changed"},
		{"sources", func(p *resourcePlan) { p.Config = []string{"other.yaml"} }, "configuration sources changed"},
		{"inputs-list", func(p *resourcePlan) { p.ConfigInputs = []string{"x.yaml"} }, "configuration inputs changed"},
		{"fileset", func(p *resourcePlan) { p.Files = nil }, "rendered file set changed"},
		{"file-sha", func(p *resourcePlan) { p.Files[0].SHA256 = "zzz" }, "file internal/invoice/model.go changed"},
		{"migration", func(p *resourcePlan) { p.Migration.Up = "changed" }, "migration changed"},
		{"migration-gone", func(p *resourcePlan) { p.Migration = nil }, "migration selection changed"},
		{"manifest-gone", func(p *resourcePlan) { p.Manifest = nil }, "manifest selection changed"},
		{"pins", func(p *resourcePlan) { p.GoMod = nil }, "go.mod pins changed"},
		{"conflicts", func(p *resourcePlan) { p.Conflicts = []string{"x"} }, "conflicts changed"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			fresh := basePlan()
			tt.mutate(fresh)
			if diff := plansEqual(basePlan(), fresh); diff != tt.want {
				t.Fatalf("diff = %q, want %q", diff, tt.want)
			}
		})
	}
}

func TestRenderManifestUpdateRejectsDuplicatesAndVersions(t *testing.T) {
	manifest := scaffold.Manifest{Module: "example.com/m", FrameworkVersion: "v0.0.0", TemplateVersion: 1}
	data := resourceData{PackageName: "invoice", Title: "Invoice"}
	if _, _, _, err := renderManifestUpdate(manifest, data, map[string]string{"internal/invoice/model.go": "abc"}); err != nil {
		t.Fatalf("v1 update: %v", err)
	}
	manifest.APIs = append(manifest.APIs, scaffold.GeneratedAPI{Name: "Invoice", Package: "invoice", Path: "internal/invoice", ModuleType: "invoice.Module"})
	if _, _, _, err := renderManifestUpdate(manifest, data, nil); err == nil {
		t.Fatal("duplicate package accepted")
	}
	manifest.TemplateVersion = scaffold.TemplateVersionFiles
	const digest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	entry, _, _, err := renderManifestUpdate(scaffold.Manifest{Module: "example.com/m", FrameworkVersion: "v0.0.0", TemplateVersion: 2}, data, map[string]string{"internal/invoice/model.go": digest})
	if err != nil {
		t.Fatalf("v2 update: %v", err)
	}
	if entry.Files["internal/invoice/model.go"] != digest {
		t.Fatalf("v2 digests = %v", entry.Files)
	}
}

func TestPathOutsideRootBoundaries(t *testing.T) {
	root := t.TempDir()
	for _, tc := range []struct {
		joined string
		want   bool
	}{
		{filepath.Join(root, "internal", "x"), false},
		{filepath.Join(root, "..", "escape"), true},
		{root, false},
	} {
		got, err := pathOutsideRoot(root, tc.joined)
		if err != nil || got != tc.want {
			t.Fatalf("pathOutsideRoot(%q) = %v, %v; want %v", tc.joined, got, err, tc.want)
		}
	}
}

func TestApplyPinsNoops(t *testing.T) {
	dir := t.TempDir()
	if err := applyPins(dir, nil); err != nil {
		t.Fatalf("nil pins: %v", err)
	}
	// Missing go.mod preserves legacy no-op behavior.
	if err := applyPins(dir, []modRequire{{Path: "gorm.io/gorm", Version: "v1.26.0"}}); err != nil {
		t.Fatalf("missing go.mod: %v", err)
	}
}

func TestGenerationConfigPlanSourcesRejectsEmpty(t *testing.T) {
	if got := generationConfigPlanSources(t.TempDir(), []string{""}); got != nil {
		t.Fatalf("empty config path produced %v", got)
	}
}

func TestPlanMigrationSkipsDisabledDatabase(t *testing.T) {
	plan := &resourcePlan{Kind: "api", ProjectRoot: t.TempDir()}
	data := resourceData{PackageName: "invoice", Title: "Invoice", RouteName: "invoice"}
	if err := planMigration(data, generatedAPIDatabase{}, plan); err != nil {
		t.Fatalf("disabled migration: %v", err)
	}
	if plan.Migration != nil {
		t.Fatal("disabled database planned a migration")
	}
}

func TestPreviewTextFormat(t *testing.T) {
	project := previewProject(t, "database:\n  enabled: false\n")
	t.Setenv("BEAR_ENV", "")
	t.Setenv("GIN_MODE", "")
	stdout, stderr, code := executeIn(t, project, "gen", "api", "invoice", "--dry-run")
	if code != 0 {
		t.Fatalf("text preview exit = %d\n%s", code, stdout)
	}
	for _, want := range []string{"preview api invoice", "migration: none", "internal/invoice/controller.go"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("text preview missing %q:\n%s", want, stdout)
		}
	}
	// Disabled-database hints stay on stderr, keeping stdout human-stable.
	if !strings.Contains(stderr, "Warning:") {
		t.Fatalf("text preview missing stderr warning: %q", stderr)
	}
}

func TestApplyRejectsMalformedPlans(t *testing.T) {
	project := previewProject(t, "")
	write := func(name, contents string) string {
		path := filepath.Join(project, name)
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	t.Chdir(project)
	badTarget, err := jsonMarshal(map[string]any{
		"schema_version": 1, "dry_run": true,
		"plan": map[string]any{"kind": "dto", "name": "x", "target": "internal/y", "project_root": project},
	})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		file string
		want string
	}{
		{"missing", filepath.Join(project, "nope.json"), "read plan"},
		{"bad-json", write("bad.json", "{oops"), "decode plan"},
		{"bad-schema", write("schema.json", `{"schema_version":99,"dry_run":true,"plan":null}`), "schema version"},
		{"not-dry-run", write("real.json", `{"schema_version":1,"dry_run":false,"plan":null}`), "not a dry-run"},
		{"bad-kind", write("kind.json", `{"schema_version":1,"dry_run":true,"plan":{"kind":"service","name":"x","target":"internal/x"}}`), "unsupported kind"},
		{"cross-project", write("cross.json", `{"schema_version":1,"dry_run":true,"plan":{"kind":"dto","name":"x","target":"internal/x","project_root":"/elsewhere"}}`), "not this project"},
		{"bad-target", write("target.json", badTarget), "invalid target"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr strings.Builder
			code := Execute([]string{"gen", "apply", "--plan", tt.file}, &stdout, &stderr)
			if code == 0 {
				t.Fatal("malformed apply exited 0")
			}
			if !strings.Contains(stderr.String(), tt.want) {
				t.Fatalf("stderr missing %q:\n%s", tt.want, stderr.String())
			}
		})
	}
	if _, err := os.Stat(filepath.Join(project, "internal")); !os.IsNotExist(err) {
		t.Fatal("rejected applies wrote files")
	}
}

func TestPlanResourceRejectsBadInputs(t *testing.T) {
	ctx := context.Background()
	if _, err := planResource(ctx, resourceOptions{Kind: "api", Name: "!!!", Directory: t.TempDir()}, generatedAPIDatabase{}, nil); err == nil {
		t.Fatal("bad resource name accepted")
	}
	if _, err := planResource(ctx, resourceOptions{Kind: "service", Name: "x", Directory: t.TempDir()}, generatedAPIDatabase{}, nil); err == nil {
		t.Fatal("bad kind accepted")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := planResource(cancelled, resourceOptions{Kind: "dto", Name: "x", Directory: t.TempDir()}, generatedAPIDatabase{}, nil); err == nil {
		t.Fatal("cancelled plan accepted")
	}
}

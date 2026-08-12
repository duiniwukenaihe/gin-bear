package scaffold

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestManifestRoundTripSortsGeneratedAPIs(t *testing.T) {
	root := t.TempDir()
	manifest := NewManifest("example.com/service", "v0.9.2")
	manifest.APIs = []GeneratedAPI{
		{Name: "Users", Package: "users", Path: "internal/users", ModuleType: "users.Module"},
		{Name: "Accounts", Package: "accounts", Path: "internal/accounts", ModuleType: "accounts.Module"},
	}
	if err := WriteManifest(root, manifest); err != nil {
		t.Fatal(err)
	}
	loaded, err := ReadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{loaded.APIs[0].Package, loaded.APIs[1].Package}; !reflect.DeepEqual(got, []string{"accounts", "users"}) {
		t.Fatalf("API order = %v", got)
	}
}

func TestGenerateWritesMinimalManifestAndModuleRegistry(t *testing.T) {
	root := filepath.Join(t.TempDir(), "service")
	if err := Generate(context.Background(), Options{
		Name:             "service",
		Module:           "example.com/service",
		Directory:        root,
		FrameworkVersion: "v0.9.2",
	}); err != nil {
		t.Fatal(err)
	}
	manifest, err := ReadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Module != "example.com/service" || manifest.FrameworkVersion != "v0.9.2" || len(manifest.APIs) != 0 {
		t.Fatalf("generated manifest = %#v", manifest)
	}
	registry, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(ModulesPath)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(registry), "func generatedModules() []bear.Module") {
		t.Fatalf("generated module registry is invalid:\n%s", registry)
	}
}

func TestManifestRejectsUnsafeOrAmbiguousRecords(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Manifest)
	}{
		{name: "module", mutate: func(m *Manifest) { m.Module = "../service" }},
		{name: "framework version", mutate: func(m *Manifest) { m.FrameworkVersion = "latest" }},
		{name: "template version", mutate: func(m *Manifest) { m.TemplateVersion = 2 }},
		{name: "unsafe API path", mutate: func(m *Manifest) {
			m.APIs = append(m.APIs, GeneratedAPI{Name: "Users", Package: "users", Path: "../users", ModuleType: "users.Module"})
		}},
		{name: "mismatched API path", mutate: func(m *Manifest) {
			m.APIs = append(m.APIs, GeneratedAPI{Name: "Users", Package: "users", Path: "internal/admin", ModuleType: "users.Module"})
		}},
		{name: "invalid module type", mutate: func(m *Manifest) {
			m.APIs = append(m.APIs, GeneratedAPI{Name: "Users", Package: "users", Path: "internal/users", ModuleType: "os.Exit"})
		}},
		{name: "mismatched module type", mutate: func(m *Manifest) {
			m.APIs = append(m.APIs, GeneratedAPI{Name: "Users", Package: "users", Path: "internal/users", ModuleType: "admin.Module"})
		}},
		{name: "reserved app package", mutate: func(m *Manifest) {
			m.APIs = append(m.APIs, GeneratedAPI{Name: "App", Package: "app", Path: "internal/app", ModuleType: "app.Module"})
		}},
		{name: "duplicate API", mutate: func(m *Manifest) {
			m.APIs = append(m.APIs,
				GeneratedAPI{Name: "Users", Package: "users", Path: "internal/users", ModuleType: "users.Module"},
				GeneratedAPI{Name: "Users Again", Package: "users", Path: "internal/users2", ModuleType: "users.Module"},
			)
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manifest := NewManifest("example.com/service", "v0.9.2")
			tt.mutate(&manifest)
			if err := manifest.Validate(); err == nil {
				t.Fatal("Validate() succeeded")
			}
		})
	}
}

func TestReadManifestRejectsTrailingJSON(t *testing.T) {
	root := t.TempDir()
	manifest := NewManifest("example.com/service", "v0.9.2")
	contents, err := MarshalManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, filepath.FromSlash(ManifestPath))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(contents, []byte("{}\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadManifest(root); err == nil || !strings.Contains(err.Error(), "trailing") {
		t.Fatalf("ReadManifest() error = %v, want trailing content rejection", err)
	}
}

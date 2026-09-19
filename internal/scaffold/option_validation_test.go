package scaffold

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestValidateFrameworkReplaceAcceptsAnEmptyReplacement guards the common path:
// a released framework version needs no local replacement.
func TestValidateFrameworkReplaceAcceptsAnEmptyReplacement(t *testing.T) {
	if err := validateFrameworkReplace("v0.9.2", ""); err != nil {
		t.Fatalf("validateFrameworkReplace() error = %v, want nil", err)
	}
}

// TestValidateFrameworkReplaceRejectsIncompleteDevelopmentPairs pins the two
// halves of the development contract: v0.0.0 requires a replacement, and a
// replacement requires v0.0.0. `bear new` documents both, so both must fail
// loudly rather than silently generating against the wrong framework.
func TestValidateFrameworkReplaceRejectsIncompleteDevelopmentPairs(t *testing.T) {
	tests := []struct {
		name      string
		version   string
		replacer  string
		wantInErr string
	}{
		{
			name:      "zero version without replacement",
			version:   "v0.0.0",
			replacer:  "",
			wantInErr: "requires a local framework replacement",
		},
		{
			name:      "replacement without zero version",
			version:   "v0.9.2",
			replacer:  t.TempDir(),
			wantInErr: "requires framework version v0.0.0",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateFrameworkReplace(tt.version, tt.replacer)
			if err == nil || !strings.Contains(err.Error(), tt.wantInErr) {
				t.Fatalf("validateFrameworkReplace(%q, %q) error = %v, want %q", tt.version, tt.replacer, err, tt.wantInErr)
			}
		})
	}
}

// TestValidateFrameworkReplaceRejectsUnusablePaths covers the path checks that
// run before the replacement is inspected on disk.
func TestValidateFrameworkReplaceRejectsUnusablePaths(t *testing.T) {
	tests := []struct {
		name      string
		replacer  string
		wantInErr string
	}{
		{
			name:      "relative path",
			replacer:  filepath.Join("relative", "framework"),
			wantInErr: "must be an absolute path",
		},
		{
			name:      "embedded newline",
			replacer:  filepath.Join(string(filepath.Separator), "tmp", "framework\npath"),
			wantInErr: "must be an absolute path",
		},
		{
			name:      "embedded NUL",
			replacer:  filepath.Join(string(filepath.Separator), "tmp", "framework\x00path"),
			wantInErr: "must be an absolute path",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateFrameworkReplace("v0.0.0", tt.replacer)
			if err == nil || !strings.Contains(err.Error(), tt.wantInErr) {
				t.Fatalf("validateFrameworkReplace() error = %v, want %q", err, tt.wantInErr)
			}
		})
	}
}

// TestValidateFrameworkReplaceRejectsUnusableReplacementTrees walks the on-disk
// checks in order: missing, not a directory, no readable go.mod, wrong module.
func TestValidateFrameworkReplaceRejectsUnusableReplacementTrees(t *testing.T) {
	root := t.TempDir()

	regularFile := filepath.Join(root, "file")
	if err := os.WriteFile(regularFile, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}

	withoutGoMod := filepath.Join(root, "no-gomod")
	if err := os.MkdirAll(withoutGoMod, 0o755); err != nil {
		t.Fatal(err)
	}

	foreign := filepath.Join(root, "foreign")
	if err := os.MkdirAll(foreign, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(foreign, "go.mod"), []byte("module example.com/other\n\ngo 1.25.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		replacer  string
		wantInErr string
	}{
		{name: "missing", replacer: filepath.Join(root, "missing"), wantInErr: "inspect framework replacement"},
		{name: "not a directory", replacer: regularFile, wantInErr: "is not a directory"},
		{name: "no go.mod", replacer: withoutGoMod, wantInErr: "read framework replacement module"},
		{name: "foreign module", replacer: foreign, wantInErr: "framework replacement module is"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateFrameworkReplace("v0.0.0", tt.replacer)
			if err == nil || !strings.Contains(err.Error(), tt.wantInErr) {
				t.Fatalf("validateFrameworkReplace() error = %v, want %q", err, tt.wantInErr)
			}
		})
	}
}

// TestValidateFrameworkReplaceAcceptsTheFrameworkCheckout is the positive
// control: the repository itself is a usable replacement, which is how every
// generated-project test in this package builds.
func TestValidateFrameworkReplaceAcceptsTheFrameworkCheckout(t *testing.T) {
	if err := validateFrameworkReplace("v0.0.0", repoRoot(t)); err != nil {
		t.Fatalf("validateFrameworkReplace() error = %v, want nil for the framework checkout", err)
	}
}

// TestValidateRejectsMalformedGeneratedAPINames covers the two record checks
// that the existing unsafe-record table does not reach.
func TestValidateRejectsMalformedGeneratedAPINames(t *testing.T) {
	tests := []struct {
		name      string
		api       GeneratedAPI
		wantInErr string
	}{
		{
			name:      "blank name",
			api:       GeneratedAPI{Name: "   ", Package: "users", Path: "internal/users", ModuleType: "users.Module"},
			wantInErr: "has invalid name",
		},
		{
			name:      "name with control character",
			api:       GeneratedAPI{Name: "Users\nAdmin", Package: "users", Path: "internal/users", ModuleType: "users.Module"},
			wantInErr: "has invalid name",
		},
		{
			name:      "package with uppercase",
			api:       GeneratedAPI{Name: "Users", Package: "Users", Path: "internal/Users", ModuleType: "Users.Module"},
			wantInErr: "has invalid package",
		},
		{
			name:      "package starting with a digit",
			api:       GeneratedAPI{Name: "Users", Package: "1users", Path: "internal/1users", ModuleType: "1users.Module"},
			wantInErr: "has invalid package",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manifest := NewManifest("example.com/service", "v0.9.2")
			manifest.APIs = []GeneratedAPI{tt.api}
			err := manifest.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.wantInErr) {
				t.Fatalf("Validate() error = %v, want %q", err, tt.wantInErr)
			}
		})
	}
}

// TestReadManifestReportsTheFailingStage keeps the three read stages
// distinguishable, so a corrupt manifest is not reported as a missing one.
func TestReadManifestReportsTheFailingStage(t *testing.T) {
	writeManifest := func(t *testing.T, body string) string {
		t.Helper()
		root := t.TempDir()
		path := filepath.Join(root, filepath.FromSlash(ManifestPath))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return root
	}

	tests := []struct {
		name      string
		root      func(t *testing.T) string
		wantInErr string
	}{
		{
			name:      "missing manifest",
			root:      func(t *testing.T) string { return t.TempDir() },
			wantInErr: "read scaffold manifest",
		},
		{
			name:      "malformed JSON",
			root:      func(t *testing.T) string { return writeManifest(t, "{") },
			wantInErr: "decode scaffold manifest",
		},
		{
			name: "unknown field",
			root: func(t *testing.T) string {
				return writeManifest(t, `{"module":"example.com/service","framework_version":"v0.9.2","template_version":1,"apis":[],"extra":true}`)
			},
			wantInErr: "decode scaffold manifest",
		},
		{
			name: "invalid record",
			root: func(t *testing.T) string {
				return writeManifest(t, `{"module":"../service","framework_version":"v0.9.2","template_version":1,"apis":[]}`)
			},
			wantInErr: "validate scaffold manifest",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ReadManifest(tt.root(t))
			if err == nil || !strings.Contains(err.Error(), tt.wantInErr) {
				t.Fatalf("ReadManifest() error = %v, want %q", err, tt.wantInErr)
			}
		})
	}
}

// TestMarshalManifestRefusesInvalidRecords pins that encoding validates first,
// so an invalid manifest never reaches disk.
func TestMarshalManifestRefusesInvalidRecords(t *testing.T) {
	invalid := NewManifest("../service", "v0.9.2")
	if _, err := MarshalManifest(invalid); err == nil {
		t.Fatal("MarshalManifest() succeeded for an invalid manifest")
	}
}

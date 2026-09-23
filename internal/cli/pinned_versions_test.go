package cli

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/mod/modfile"
)

// TestPinnedModuleVersionsTrackFrameworkGoMod keeps the versions `bear gen api`
// writes into a generated project aligned with the ones the framework itself
// builds against.
//
// The constants in gen.go are the only copy a compiled CLI can reach, so nothing
// else connects the two: a dependency bump in the repository's go.mod that misses
// gen.go would silently pin every new project to an older release, and the
// mismatch would only surface later as a surprising `go mod tidy` rewrite.
func TestPinnedModuleVersionsTrackFrameworkGoMod(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(repositoryRoot, "go.mod")
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	file, err := modfile.Parse(path, contents, nil)
	if err != nil {
		t.Fatal(err)
	}
	required := make(map[string]string, len(file.Require))
	for _, requirement := range file.Require {
		required[requirement.Mod.Path] = requirement.Mod.Version
	}

	for _, pinned := range []struct {
		module  string
		version string
	}{
		{module: "github.com/gin-gonic/gin", version: ginModuleVersion},
		{module: "gorm.io/gorm", version: gormModuleVersion},
	} {
		actual, ok := required[pinned.module]
		if !ok {
			t.Errorf("framework go.mod does not require %s, but bear gen api pins %s",
				pinned.module, pinned.version)
			continue
		}
		if actual != pinned.version {
			t.Errorf("bear gen api pins %s %s while the framework builds against %s; update the constant in gen.go",
				pinned.module, pinned.version, actual)
		}
	}
}

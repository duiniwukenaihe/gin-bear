package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestControllerTemplateParsesIDWithError(t *testing.T) {
	if strings.Contains(controllerTmpl, "mustParseID") {
		t.Fatal("controller template should not generate mustParseID")
	}
	if !strings.Contains(controllerTmpl, "parseID") {
		t.Fatal("controller template should generate parseID")
	}
	if !strings.Contains(controllerTmpl, "return nil, err") {
		t.Fatal("controller template should return parse errors")
	}
}

func TestGenerateAPIProducesCompilablePackage(t *testing.T) {
	dir := t.TempDir()
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repoRoot := filepath.Clean(filepath.Join(oldWd, "../../.."))
	t.Cleanup(func() {
		if err := os.Chdir(oldWd); err != nil {
			t.Fatal(err)
		}
	})
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	goMod := "module generated-smoke\n\ngo 1.25.0\n\nreplace github.com/duiniwukenaihe/gin-bear => " + repoRoot + "\n\nrequire github.com/duiniwukenaihe/gin-bear v0.0.0\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(goMod), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "pkg"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(repoRoot, "pkg", "bear"), filepath.Join(dir, "pkg", "bear")); err != nil {
		t.Fatal(err)
	}

	generateAPI("widget", "name:string")

	tidy := exec.Command("go", "mod", "tidy")
	tidy.Env = append(os.Environ(), "GOPROXY=https://goproxy.cn,direct")
	if out, err := tidy.CombinedOutput(); err != nil {
		t.Fatalf("generated module tidy failed: %v\n%s", err, string(out))
	}

	cmd := exec.Command("go", "test", "./...")
	cmd.Env = append(os.Environ(), "GOPROXY=https://goproxy.cn,direct")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("generated package should compile: %v\n%s", err, string(out))
	}
}

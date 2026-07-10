package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestRewriteGoModModuleLine(t *testing.T) {
	input := "module github.com/duiniwukenaihe/gin-bear\n\ngo 1.25.0\n"

	got := rewriteGoModModule(input, "my-app")

	want := "module my-app\n\ngo 1.25.0\n"
	if got != want {
		t.Fatalf("go.mod rewrite = %q", got)
	}
}

func TestRewriteGoImports(t *testing.T) {
	input := `package main

import (
	"bear/pkg/bear"
	"github.com/duiniwukenaihe/gin-bear/pkg/bear/gen"
)
`

	got := rewriteGoImports(input, "my-app")

	if got == input {
		t.Fatal("expected imports to change")
	}
	if want := `"my-app/pkg/bear"`; !strings.Contains(got, want) {
		t.Fatalf("missing %s in %s", want, got)
	}
	if want := `"my-app/pkg/bear/gen"`; !strings.Contains(got, want) {
		t.Fatalf("missing %s in %s", want, got)
	}
}

func TestRewriteGoModModuleAddsModuleLineWhenMissing(t *testing.T) {
	input := "go 1.25.0\n"

	got := rewriteGoModModule(input, "my-app")

	if !strings.HasPrefix(got, "module my-app\n\n") {
		t.Fatalf("unexpected module prefix: %q", got)
	}
}

func TestRewriteFileReturnsReadErrors(t *testing.T) {
	err := rewriteFile("missing.go", func(content string) string { return content })
	if err == nil {
		t.Fatal("expected missing file error")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected not exist error, got %v", err)
	}
}

func TestUpdateFileAndRewriteFileModifyContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "go.mod")
	if err := os.WriteFile(path, []byte("module bear\n"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := updateFile(path, "bear", "my-app"); err != nil {
		t.Fatalf("updateFile failed: %v", err)
	}
	if err := rewriteFile(path, strings.ToUpper); err != nil {
		t.Fatalf("rewriteFile failed: %v", err)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "MODULE MY-APP\n" {
		t.Fatalf("unexpected rewritten content: %q", string(content))
	}
}

func TestCommandArgumentValidationCoversCliErrorPaths(t *testing.T) {
	tests := []struct {
		name string
		cmd  *cobra.Command
		args []string
	}{
		{
			name: "new requires project name",
			cmd:  newCmd,
			args: nil,
		},
		{
			name: "gen requires type and name",
			cmd:  genCmd,
			args: []string{"api"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cmd.Args(tt.cmd, tt.args)
			if err == nil {
				t.Fatal("expected args validation error")
			}
		})
	}
}

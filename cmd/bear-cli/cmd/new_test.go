package cmd

import (
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestLegacyNewCommandIsThinSharedCLIDelegate(t *testing.T) {
	contents, err := os.ReadFile("new.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	if !strings.Contains(text, `var newCmd = legacyCommand("new")`) {
		t.Fatalf("legacy new command does not delegate to internal/cli:\n%s", text)
	}
	for _, forbidden := range []string{"func updateFile(", "func rewriteFile(", "func rewriteGoModModule(", "func rewriteGoImports("} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("legacy new command retains unreachable helper %q", forbidden)
		}
	}
}

func TestCommandArgumentValidationCoversCLIErrorPaths(t *testing.T) {
	tests := []struct {
		name string
		cmd  *cobra.Command
		args []string
	}{
		{name: "new requires project name", cmd: newCmd},
		{name: "gen requires type and name", cmd: genCmd, args: []string{"api"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.cmd.Args(tt.cmd, tt.args); err == nil {
				t.Fatal("expected args validation error")
			}
		})
	}
}

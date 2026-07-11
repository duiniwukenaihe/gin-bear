package cmd

import (
	"os"

	"github.com/duiniwukenaihe/gin-bear/internal/cli"
	"github.com/spf13/cobra"
)

var rootCmd = cli.NewRootCommand(os.Stdout, os.Stderr)

// Execute runs the shared CLI with process arguments.
// Deprecated: use cli.Execute for in-process execution.
func Execute() {
	if code := cli.Execute(os.Args[1:], os.Stdout, os.Stderr); code != 0 {
		os.Exit(code)
	}
}

func legacyCommand(name string) *cobra.Command {
	for _, command := range rootCmd.Commands() {
		if command.Name() == name {
			return command
		}
	}
	panic("missing shared command: " + name)
}

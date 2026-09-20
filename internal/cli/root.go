package cli

import (
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

func Execute(args []string, stdout, stderr io.Writer) int {
	root := NewRootCommand(stdout, stderr)
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		var exitErr *exitError
		if errors.As(err, &exitErr) {
			fmt.Fprintln(stderr, exitErr)
			return exitErr.code
		}
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

// NewRootCommand is exported for the deprecated cmd/bear-cli/cmd package.
// New integrations should call Execute.
func NewRootCommand(stdout, stderr io.Writer) *cobra.Command {
	root := &cobra.Command{
		Use:           "bear",
		Short:         "Bear CLI - a scaffolding tool for gin-bear",
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.AddCommand(newCommand(), genCommand(), doctorCommand(), agentCommand())
	return root
}

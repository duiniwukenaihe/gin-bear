package cli

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/duiniwukenaihe/gin-bear/internal/scaffold"
	"github.com/duiniwukenaihe/gin-bear/pkg/bear"
	"github.com/spf13/cobra"
)

const developmentFrameworkVersion = "v0.10.0-rc.1"

func defaultFrameworkVersion() string {
	version := strings.TrimSpace(bear.Version)
	if version == "" || version == "dev" {
		return developmentFrameworkVersion
	}
	if !strings.HasPrefix(version, "v") {
		version = "v" + version
	}
	return version
}

func newCommand() *cobra.Command {
	var module string
	var directory string
	var frameworkVersion string
	command := &cobra.Command{
		Use:   "new <project_name>",
		Short: "Create a new gin-bear project",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			projectModule := module
			if projectModule == "" {
				projectModule = name
			}
			projectDirectory := directory
			if projectDirectory == "" {
				projectDirectory = name
			}
			projectDirectory, err := filepath.Abs(projectDirectory)
			if err != nil {
				return fmt.Errorf("resolve project directory: %w", err)
			}
			if err := scaffold.Generate(cmd.Context(), scaffold.Options{
				Name:             name,
				Module:           projectModule,
				Directory:        projectDirectory,
				FrameworkVersion: frameworkVersion,
			}); err != nil {
				return fmt.Errorf("create project: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Created %s in %s\n", name, projectDirectory)
			return nil
		},
	}
	command.Flags().StringVar(&module, "module", "", "Go module path (defaults to project name)")
	command.Flags().StringVarP(&directory, "directory", "d", "", "destination directory (defaults to project name)")
	command.Flags().StringVar(&frameworkVersion, "framework-version", defaultFrameworkVersion(), "gin-bear framework version")
	return command
}

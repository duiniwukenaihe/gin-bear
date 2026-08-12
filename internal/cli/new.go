package cli

import (
	"fmt"
	"path/filepath"
	"runtime/debug"
	"strings"

	"github.com/duiniwukenaihe/gin-bear/internal/scaffold"
	"github.com/duiniwukenaihe/gin-bear/pkg/bear"
	"github.com/spf13/cobra"
	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"
)

func defaultFrameworkVersion() string {
	moduleVersion := ""
	if info, ok := debug.ReadBuildInfo(); ok {
		moduleVersion = info.Main.Version
	}
	return resolveFrameworkVersion(bear.Version, moduleVersion)
}

func resolveFrameworkVersion(linkedVersion, moduleVersion string) string {
	for _, candidate := range []string{linkedVersion, moduleVersion} {
		version := strings.TrimSpace(candidate)
		if version == "" || version == "dev" || version == "(devel)" {
			continue
		}
		if !strings.HasPrefix(version, "v") {
			version = "v" + version
		}
		if !semver.IsValid(version) || strings.Contains(version, "+dirty") || module.IsPseudoVersion(strings.TrimSuffix(version, "+dirty")) {
			continue
		}
		return version
	}
	return ""
}

func newCommand() *cobra.Command {
	var module string
	var directory string
	var frameworkVersion string
	var frameworkReplace string
	generatorVersion := defaultFrameworkVersion()
	command := &cobra.Command{
		Use:   "new <project_name>",
		Short: "Create a new gin-bear project",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dependencyVersion, localReplace, err := scaffoldFrameworkDependency(generatorVersion, frameworkVersion, frameworkReplace)
			if err != nil {
				return err
			}
			name := args[0]
			projectModule := module
			if projectModule == "" {
				projectModule = name
			}
			projectDirectory := directory
			if projectDirectory == "" {
				projectDirectory = name
			}
			projectDirectory, err = filepath.Abs(projectDirectory)
			if err != nil {
				return fmt.Errorf("resolve project directory: %w", err)
			}
			if err := scaffold.Generate(cmd.Context(), scaffold.Options{
				Name:             name,
				Module:           projectModule,
				Directory:        projectDirectory,
				FrameworkVersion: dependencyVersion,
				FrameworkReplace: localReplace,
			}); err != nil {
				return fmt.Errorf("create project: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Created %s in %s\n", name, projectDirectory)
			return nil
		},
	}
	command.Flags().StringVar(&module, "module", "", "Go module path (defaults to project name)")
	command.Flags().StringVarP(&directory, "directory", "d", "", "destination directory (defaults to project name)")
	command.Flags().StringVar(&frameworkVersion, "framework-version", generatorVersion, "gin-bear framework version")
	command.Flags().StringVar(&frameworkReplace, "framework-replace", "", "local gin-bear checkout for development templates")
	return command
}

func scaffoldFrameworkDependency(generatorVersion, requestedVersion, localReplace string) (string, string, error) {
	if generatorVersion == "" {
		if requestedVersion == "" {
			return "", "", fmt.Errorf("development generator requires --framework-version dev and --framework-replace pointing to the unreleased HEAD checkout")
		}
		if requestedVersion != "dev" {
			return "", "", fmt.Errorf("development generator templates target unreleased HEAD; requested framework version %q would mix released dependencies with HEAD templates; use --framework-version dev --framework-replace /absolute/path/to/gin-bear", requestedVersion)
		}
		if strings.TrimSpace(localReplace) == "" {
			return "", "", fmt.Errorf("development generator requires --framework-replace with --framework-version dev")
		}
		absoluteReplace, err := filepath.Abs(localReplace)
		if err != nil {
			return "", "", fmt.Errorf("resolve framework replacement: %w", err)
		}
		return "v0.0.0", absoluteReplace, nil
	}
	if requestedVersion != generatorVersion {
		return "", "", fmt.Errorf("generator templates target %s; requested framework version %q must match", generatorVersion, requestedVersion)
	}
	if strings.TrimSpace(localReplace) != "" {
		return "", "", fmt.Errorf("released generator templates target %s and do not accept --framework-replace", generatorVersion)
	}
	return generatorVersion, "", nil
}

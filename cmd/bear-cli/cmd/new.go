package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

var repoURL string

func init() {
	newCmd.Flags().StringVarP(&repoURL, "repo", "r", "https://github.com/duiniwukenaihe/gin-bear.git", "Repository URL to clone from")
	rootCmd.AddCommand(newCmd)
}

var newCmd = &cobra.Command{
	Use:   "new [project_name]",
	Short: "Create a new project",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		projectName := args[0]
		fmt.Printf("Creating project %s...\n", projectName)

		if _, err := os.Stat(projectName); !os.IsNotExist(err) {
			fmt.Printf("Directory %s already exists\n", projectName)
			os.Exit(1)
		}

		// 1. Git Clone
		fmt.Println("Cloning template...")
		gitCmd := exec.Command("git", "clone", "--depth=1", repoURL, projectName)
		gitCmd.Stdout = os.Stdout
		gitCmd.Stderr = os.Stderr
		if err := gitCmd.Run(); err != nil {
			fmt.Printf("Failed to clone repository: %v\n", err)
			os.Exit(1)
		}

		// 2. Remove .git
		os.RemoveAll(filepath.Join(projectName, ".git"))

		// 3. Update go.mod
		fmt.Println("Updating go.mod...")
		goModPath := filepath.Join(projectName, "go.mod")
		rewriteFile(goModPath, func(content string) string {
			return rewriteGoModModule(content, projectName)
		})

		// 4. Replace imports in all .go files
		fmt.Println("Renaming imports...")
		err := filepath.Walk(projectName, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if !info.IsDir() && strings.HasSuffix(path, ".go") {
				rewriteFile(path, func(content string) string {
					return rewriteGoImports(content, projectName)
				})
			}
			return nil
		})
		if err != nil {
			fmt.Printf("Failed to rename imports: %v\n", err)
		}

		// 5. Update build metadata ldflags package paths in generated artifacts.
		for _, path := range buildMetadataRewritePaths(projectName) {
			if err := rewriteFileIfExists(path, func(content string) string {
				return rewriteBuildMetadataPackage(content, projectName)
			}); err != nil {
				fmt.Printf("Failed to update build metadata in %s: %v\n", path, err)
			}
		}

		fmt.Printf("\nProject %s created successfully!\n", projectName)
		fmt.Printf("cd %s\n", projectName)
		fmt.Println("go mod tidy")
		fmt.Println("go run main.go")
	},
}

func updateFile(path, old, new string) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	newContent := strings.ReplaceAll(string(content), old, new)
	return os.WriteFile(path, []byte(newContent), 0644)
}

func rewriteFile(path string, rewrite func(string) string) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(rewrite(string(content))), 0644)
}

func rewriteFileIfExists(path string, rewrite func(string) string) error {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return rewriteFile(path, rewrite)
}

func rewriteGoModModule(content, moduleName string) string {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "module ") {
			lines[i] = "module " + moduleName
			return strings.Join(lines, "\n")
		}
	}
	return "module " + moduleName + "\n\n" + content
}

func rewriteGoImports(content, moduleName string) string {
	content = strings.ReplaceAll(content, "\"bear/", "\""+moduleName+"/")
	content = strings.ReplaceAll(content, "\"github.com/duiniwukenaihe/gin-bear/", "\""+moduleName+"/")
	return content
}

func rewriteBuildMetadataPackage(content, moduleName string) string {
	return strings.ReplaceAll(content, "github.com/duiniwukenaihe/gin-bear/pkg/bear", moduleName+"/pkg/bear")
}

func buildMetadataRewritePaths(projectName string) []string {
	return []string{
		filepath.Join(projectName, "Dockerfile"),
		filepath.Join(projectName, ".github", "workflows", "ci.yml"),
	}
}

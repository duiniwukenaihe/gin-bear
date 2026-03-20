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
	newCmd.Flags().StringVarP(&repoURL, "repo", "r", "https://github.com/polarbear-workshop/gin-bear.git", "Repository URL to clone from")
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
		updateFile(goModPath, "module bear", "module "+projectName)

		// 4. Replace imports in all .go files
		fmt.Println("Renaming imports...")
		err := filepath.Walk(projectName, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if !info.IsDir() && strings.HasSuffix(path, ".go") {
				// Replace "bear/pkg" with "project/pkg"
				// Be careful with "bear" string.
				// Our module is named "bear". imports are like "bear/pkg/bear".
				// So replacing "\"bear/" with "\"projectName/" is safe.
				updateFile(path, "\"bear/", "\""+projectName+"/")
			}
			return nil
		})
		if err != nil {
			fmt.Printf("Failed to rename imports: %v\n", err)
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

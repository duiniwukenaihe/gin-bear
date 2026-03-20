package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "bear",
	Short: "Bear CLI - A scaffolding tool for gin-bear",
	Long: `Bear CLI is a productivity tool designed to help developers quickly build applications based on the gin-bear framework.
It supports project initialization, code generation, and dev mode.`,
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

// Package main demonstrates the error-returning configuration API required for
// production startup. See docs/migration-v0.9.1-to-v0.9.2.md for configuration
// changes that must be completed before deploying v0.9.2.
package main

import (
	"fmt"
	"os"

	"github.com/duiniwukenaihe/gin-bear/pkg/bear"
)

func loadProductionConfig(path string) (*bear.SysConfig, error) {
	config, err := bear.LoadConfig(path)
	if err != nil {
		return nil, fmt.Errorf("load production configuration: %w", err)
	}
	return config, nil
}

func main() {
	path := os.Getenv("BEAR_CONFIG")
	if path == "" {
		path = "application-prod.yaml"
	}
	if _, err := loadProductionConfig(path); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

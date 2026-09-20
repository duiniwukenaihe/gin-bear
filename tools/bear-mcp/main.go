package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// bear-dev-mcp: optional local development bridge. stdio only, read-only
// tools, no production credentials, no writes. The host binds everything:
//
//	BEAR_CLI_BIN      absolute bear binary (default: "bear" on PATH, pinned once)
//	BEAR_PROJECT_ROOTS  allowlist of project roots (filepath.ListSeparator-joined)
//
// Nothing else is configurable by the model.
func main() {
	if err := run(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	cliBin, err := resolveCLIBin(os.Getenv("BEAR_CLI_BIN"))
	if err != nil {
		return err
	}
	rootsEnv := strings.TrimSpace(os.Getenv("BEAR_PROJECT_ROOTS"))
	if rootsEnv == "" {
		return fmt.Errorf("BEAR_PROJECT_ROOTS is required (filepath.ListSeparator-joined allowlist)")
	}
	var roots []string
	for _, root := range filepath.SplitList(rootsEnv) {
		if strings.TrimSpace(root) != "" {
			roots = append(roots, root)
		}
	}
	server, err := NewServer(Config{CLIBin: cliBin, Roots: roots, Timeout: 60 * time.Second})
	if err != nil {
		return err
	}
	return server.mcpServer.Run(ctx, &mcp.StdioTransport{})
}

func resolveCLIBin(configured string) (string, error) {
	if strings.TrimSpace(configured) != "" {
		abs, err := filepath.Abs(configured)
		if err != nil {
			return "", fmt.Errorf("invalid BEAR_CLI_BIN: %w", err)
		}
		info, err := os.Stat(abs)
		if err != nil {
			return "", fmt.Errorf("BEAR_CLI_BIN %q is not accessible: %w", configured, err)
		}
		if info.IsDir() || info.Mode()&0o111 == 0 {
			return "", fmt.Errorf("BEAR_CLI_BIN %q is not executable", configured)
		}
		return abs, nil
	}
	found, err := exec.LookPath("bear")
	if err != nil {
		return "", fmt.Errorf("bear not on PATH and BEAR_CLI_BIN unset")
	}
	return found, nil
}

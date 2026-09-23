package main

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Protocol limits: bounded inputs, bounded outputs, bounded time. The model
// never supplies shell strings, command paths, or working directories.
const (
	maxNameLen     = 256
	maxFieldsLen   = 4096
	maxConfigFiles = 8
	maxPathLen     = 1024
	// maxOutput caps one CLI invocation's stdout; larger output is an error,
	// never a silent truncation.
	maxOutputBytes = 256 << 10
	defaultTimeout = 60 * time.Second
	serverName     = "bear-dev-mcp"
	serverVersion  = "v1.0.0"
)

// Config binds the server to a fixed CLI binary and fixed project roots.
// Everything else is derived from typed tool parameters.
type Config struct {
	CLIBin  string
	Roots   []string
	Timeout time.Duration
}

// Server wraps the MCP server with its bound configuration.
type Server struct {
	mcpServer *mcp.Server
	cliBin    string
	roots     []string
	timeout   time.Duration
}

// NewServer validates the binding and registers exactly the three read-only
// development tools. It performs no I/O.
func NewServer(cfg Config) (*Server, error) {
	if strings.TrimSpace(cfg.CLIBin) == "" {
		return nil, fmt.Errorf("CLI binary path is required")
	}
	if !filepath.IsAbs(cfg.CLIBin) {
		return nil, fmt.Errorf("CLI binary path %q must be absolute", cfg.CLIBin)
	}
	if len(cfg.Roots) == 0 {
		return nil, fmt.Errorf("at least one project root is required")
	}
	roots := make([]string, 0, len(cfg.Roots))
	for _, root := range cfg.Roots {
		resolved, err := resolveRoot(root)
		if err != nil {
			return nil, err
		}
		roots = append(roots, resolved)
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	server := &Server{
		mcpServer: mcp.NewServer(&mcp.Implementation{Name: serverName, Version: serverVersion}, nil),
		cliBin:    cfg.CLIBin,
		roots:     roots,
		timeout:   timeout,
	}
	mcp.AddTool(server.mcpServer, &mcp.Tool{Name: "project_info", Description: "Read-only project summary: module, manifest, config sources. No writes, no subprocess."}, server.projectInfo)
	mcp.AddTool(server.mcpServer, &mcp.Tool{Name: "doctor", Description: "Run bear doctor --format json (static) in a bound project. Read-only; --probe dials dependencies with a timeout."}, server.doctor)
	mcp.AddTool(server.mcpServer, &mcp.Tool{Name: "gen_preview", Description: "Preview code generation (always --dry-run --format json) in a bound project. Never writes."}, server.genPreview)
	return server, nil
}

// ToolNames reports the stable protocol tool list.
func (s *Server) ToolNames() []string {
	return []string{"project_info", "doctor", "gen_preview"}
}

func resolveRoot(root string) (string, error) {
	if strings.TrimSpace(root) == "" || len(root) > maxPathLen {
		return "", fmt.Errorf("invalid project root")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("invalid project root: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("project root %q is not accessible: %w", root, err)
	}
	return resolved, nil
}

// bindDir resolves a model-supplied directory against the bound roots. It
// rejects relative paths, overlong input, traversal, and anything outside
// the allowlist, after symlink resolution.
func (s *Server) bindDir(input string) (string, error) {
	if len(input) == 0 || len(input) > maxPathLen {
		return "", fmt.Errorf("invalid project_dir")
	}
	if !filepath.IsAbs(input) {
		return "", fmt.Errorf("project_dir %q must be absolute", input)
	}
	clean := filepath.Clean(input)
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return "", fmt.Errorf("project_dir %q is not accessible", input)
	}
	for _, root := range s.roots {
		if resolved == root || strings.HasPrefix(resolved, root+string(filepath.Separator)) {
			return resolved, nil
		}
	}
	return "", fmt.Errorf("project_dir %q is outside the bound roots", input)
}

// runCLI executes the pinned binary with a fixed argv array in dir. The model
// influences only typed fields already validated by the caller.
func (s *Server) runCLI(ctx context.Context, dir string, argv ...string) ([]byte, error) {
	timeoutCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	cmd := exec.CommandContext(timeoutCtx, s.cliBin, argv...)
	cmd.Dir = dir
	// No stdin, no shell, no environment passthrough beyond the process env.
	cmd.Stdin = nil
	output, err := cmd.Output()
	if err != nil {
		if errors.Is(timeoutCtx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("CLI timed out after %s", s.timeout)
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, fmt.Errorf("CLI failed: %s", cappedString(exitErr.Stderr, 2048))
		}
		return nil, fmt.Errorf("CLI failed: %w", err)
	}
	if int64(len(output)) > maxOutputBytes {
		return nil, fmt.Errorf("CLI output %d bytes exceeds the %d-byte limit", len(output), maxOutputBytes)
	}
	return output, nil
}

func cappedString(data []byte, limit int) string {
	text := string(data)
	if len(text) > limit {
		text = text[:limit] + "...[truncated]"
	}
	return text
}

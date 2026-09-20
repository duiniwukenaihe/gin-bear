package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ProjectInfoInput selects a bound project. No other influence exists.
type ProjectInfoInput struct {
	ProjectDir string `json:"project_dir" jsonschema:"absolute path of the project, must sit under a bound root"`
}

// ProjectInfoOutput is a read-only summary. Secrets are never included.
type ProjectInfoOutput struct {
	Module       string   `json:"module"`
	Manifest     bool     `json:"manifest"`
	Framework    string   `json:"framework,omitempty"`
	Template     int      `json:"template,omitempty"`
	APIs         int      `json:"apis,omitempty"`
	ConfigFiles  []string `json:"config_files"`
	HasBearGoMod bool     `json:"has_go_mod"`
}

func (s *Server) projectInfo(_ context.Context, _ *mcp.CallToolRequest, input ProjectInfoInput) (*mcp.CallToolResult, ProjectInfoOutput, error) {
	var output ProjectInfoOutput
	dir, err := s.bindDir(input.ProjectDir)
	if err != nil {
		return nil, output, err
	}
	module, err := readGoModModule(filepath.Join(dir, "go.mod"))
	if err != nil {
		return nil, output, fmt.Errorf("project_info: %w", err)
	}
	output.Module = module
	output.HasBearGoMod = true
	for _, name := range []string{"application.yaml", "config.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			output.ConfigFiles = append(output.ConfigFiles, name)
		}
	}
	if manifest, err := readManifestSummary(filepath.Join(dir, ".bear", "scaffold.json")); err == nil {
		output.Manifest = true
		output.Framework = manifest.FrameworkVersion
		output.Template = manifest.TemplateVersion
		output.APIs = len(manifest.APIs)
	}
	return nil, output, nil
}

type manifestSummary struct {
	FrameworkVersion string `json:"framework_version"`
	TemplateVersion  int    `json:"template_version"`
	APIs             []struct {
		Package string `json:"package"`
	} `json:"apis"`
}

func readManifestSummary(path string) (manifestSummary, error) {
	var summary manifestSummary
	contents, err := os.ReadFile(path)
	if err != nil {
		return summary, err
	}
	if err := json.Unmarshal(contents, &summary); err != nil {
		return summary, fmt.Errorf("invalid manifest: %w", err)
	}
	return summary, nil
}

func readGoModModule(path string) (string, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("unreadable go.mod: %w", err)
	}
	for _, line := range strings.Split(string(contents), "\n") {
		line = strings.TrimSpace(line)
		if module, ok := strings.CutPrefix(line, "module "); ok {
			module = strings.TrimSpace(module)
			if module == "" {
				break
			}
			return module, nil
		}
	}
	return "", fmt.Errorf("go.mod has no module line")
}

// DoctorInput mirrors `bear doctor` flags as typed fields.
type DoctorInput struct {
	ProjectDir string `json:"project_dir" jsonschema:"absolute path of the project, must sit under a bound root"`
	Probe      bool   `json:"probe,omitempty" jsonschema:"also dial dependencies with a bounded timeout"`
}

func (s *Server) doctor(ctx context.Context, _ *mcp.CallToolRequest, input DoctorInput) (*mcp.CallToolResult, map[string]any, error) {
	dir, err := s.bindDir(input.ProjectDir)
	if err != nil {
		return nil, nil, err
	}
	argv := []string{"doctor", "--format", "json"}
	if input.Probe {
		argv = append(argv, "--probe")
	}
	output, err := s.runCLI(ctx, dir, argv...)
	if err != nil {
		return nil, nil, err
	}
	var report map[string]any
	if err := json.Unmarshal(output, &report); err != nil {
		return nil, nil, fmt.Errorf("doctor returned invalid JSON: %w", err)
	}
	return nil, report, nil
}

// GenPreviewInput mirrors `bear gen` flags as typed fields. The handler
// always appends --dry-run --format json itself; preview can never write.
type GenPreviewInput struct {
	ProjectDir string   `json:"project_dir" jsonschema:"absolute path of the project, must sit under a bound root"`
	Kind       string   `json:"kind" jsonschema:"api, model, or dto"`
	Name       string   `json:"name" jsonschema:"resource name"`
	Fields     string   `json:"fields,omitempty" jsonschema:"fields as name:type pairs"`
	Config     []string `json:"config,omitempty" jsonschema:"generation config files, project-relative, api only"`
}

func (s *Server) genPreview(ctx context.Context, _ *mcp.CallToolRequest, input GenPreviewInput) (*mcp.CallToolResult, map[string]any, error) {
	dir, err := s.bindDir(input.ProjectDir)
	if err != nil {
		return nil, nil, err
	}
	kind := strings.ToLower(strings.TrimSpace(input.Kind))
	if kind != "api" && kind != "model" && kind != "dto" {
		return nil, nil, fmt.Errorf("invalid kind %q", input.Kind)
	}
	if len(input.Name) == 0 || len(input.Name) > maxNameLen {
		return nil, nil, fmt.Errorf("invalid name")
	}
	if len(input.Fields) > maxFieldsLen {
		return nil, nil, fmt.Errorf("fields exceed the %d-byte limit", maxFieldsLen)
	}
	argv := []string{"gen", kind, input.Name}
	if input.Fields != "" {
		argv = append(argv, "--fields", input.Fields)
	}
	if len(input.Config) > maxConfigFiles {
		return nil, nil, fmt.Errorf("too many config files (max %d)", maxConfigFiles)
	}
	for _, name := range input.Config {
		if len(name) == 0 || len(name) > maxPathLen || filepath.IsAbs(name) || filepath.Clean(name) != name {
			return nil, nil, fmt.Errorf("invalid config file %q: must be a clean project-relative path", name)
		}
		for _, element := range strings.Split(filepath.ToSlash(name), "/") {
			if element == ".." {
				return nil, nil, fmt.Errorf("invalid config file %q: must stay inside the project", name)
			}
		}
		argv = append(argv, "--config", name)
	}
	// Fixed suffix: the model cannot reach argv any other way.
	argv = append(argv, "--dry-run", "--format", "json")
	output, err := s.runCLI(ctx, dir, argv...)
	if err != nil {
		return nil, nil, err
	}
	var preview map[string]any
	if err := json.Unmarshal(output, &preview); err != nil {
		return nil, nil, fmt.Errorf("preview returned invalid JSON: %w", err)
	}
	return nil, preview, nil
}

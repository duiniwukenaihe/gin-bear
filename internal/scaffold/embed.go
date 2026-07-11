package scaffold

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"go/format"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"
	"unicode"
)

const templateRoot = "template"

var semanticVersionPattern = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z]+([.-][0-9A-Za-z]+)*)?(\+[0-9A-Za-z]+([.-][0-9A-Za-z]+)*)?$`)

type Options struct {
	Name             string
	Module           string
	Directory        string
	FrameworkVersion string
}

//go:embed template/**
var templateFS embed.FS

func Generate(ctx context.Context, opts Options) error {
	if err := validateOptions(opts); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := os.Lstat(opts.Directory); err == nil {
		return fmt.Errorf("destination %q already exists", opts.Directory)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect destination %q: %w", opts.Directory, err)
	}

	parent := filepath.Dir(opts.Directory)
	if err := os.MkdirAll(parent, 0755); err != nil {
		return fmt.Errorf("create destination parent: %w", err)
	}
	temporary, err := os.MkdirTemp(parent, "."+filepath.Base(opts.Directory)+".tmp-")
	if err != nil {
		return fmt.Errorf("create temporary scaffold: %w", err)
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(temporary)
		}
	}()

	if err := renderTemplateTree(ctx, templateFS, templateRoot, temporary, opts); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(temporary, opts.Directory); err != nil {
		return fmt.Errorf("publish scaffold: %w", err)
	}
	published = true
	return nil
}

func validateOptions(opts Options) error {
	values := []struct {
		name  string
		value string
	}{
		{name: "name", value: opts.Name},
		{name: "module", value: opts.Module},
		{name: "directory", value: opts.Directory},
		{name: "framework version", value: opts.FrameworkVersion},
	}
	for _, value := range values {
		if strings.TrimSpace(value.value) == "" {
			return fmt.Errorf("%s is required", value.name)
		}
		if strings.ContainsAny(value.value, "\r\n\x00") {
			return fmt.Errorf("%s contains invalid characters", value.name)
		}
	}
	if !validModulePath(opts.Module) {
		return fmt.Errorf("module %q is invalid", opts.Module)
	}
	if !semanticVersionPattern.MatchString(opts.FrameworkVersion) {
		return fmt.Errorf("framework version %q is invalid", opts.FrameworkVersion)
	}
	return nil
}

func validModulePath(module string) bool {
	if strings.ContainsAny(module, " \t\\") || strings.HasPrefix(module, ".") || strings.HasSuffix(module, "/") {
		return false
	}
	for _, segment := range strings.Split(module, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
		for _, r := range segment {
			if unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("-._~", r) {
				continue
			}
			return false
		}
	}
	return true
}

func renderTemplateTree(ctx context.Context, source fs.FS, root, destination string, data Options) error {
	return fs.WalkDir(source, root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return fmt.Errorf("resolve template path %q: %w", path, err)
		}
		if relative == "." {
			return nil
		}
		target := filepath.Join(destination, strings.TrimSuffix(relative, ".tmpl"))
		if entry.IsDir() {
			if err := os.MkdirAll(target, 0755); err != nil {
				return fmt.Errorf("create scaffold directory %q: %w", target, err)
			}
			return nil
		}

		contents, err := fs.ReadFile(source, path)
		if err != nil {
			return fmt.Errorf("read template %q: %w", path, err)
		}
		rendered, err := renderTemplate(path, contents, data)
		if err != nil {
			return err
		}
		if strings.HasSuffix(target, ".go") {
			rendered, err = format.Source(rendered)
			if err != nil {
				return fmt.Errorf("format generated Go file %q: %w", relative, err)
			}
		}
		if err := os.WriteFile(target, rendered, 0644); err != nil {
			return fmt.Errorf("write scaffold file %q: %w", target, err)
		}
		return nil
	})
}

func renderTemplate(name string, source []byte, data Options) ([]byte, error) {
	tmpl, err := template.New(name).Option("missingkey=error").Parse(string(source))
	if err != nil {
		return nil, fmt.Errorf("parse template %q: %w", name, err)
	}
	var output strings.Builder
	if err := tmpl.Execute(&output, data); err != nil {
		return nil, fmt.Errorf("render template %q: %w", name, err)
	}
	return []byte(output.String()), nil
}

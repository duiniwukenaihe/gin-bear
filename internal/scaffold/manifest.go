package scaffold

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/mod/module"
)

const (
	// TemplateVersion is the manifest version new projects start with.
	TemplateVersion = 1
	// TemplateVersionFiles adds per-API content digests for generator-owned
	// files. Readers accept v1 and v2; writers stay on v1 unless an upgrade
	// path (gen apply) explicitly moves a manifest to v2.
	TemplateVersionFiles = 2
	ManifestPath         = ".bear/scaffold.json"
	ModulesPath          = "internal/app/modules_gen.go"
)

var (
	generatedPackageName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	generatedModuleType  = regexp.MustCompile(`^[a-z][a-z0-9_]*\.Module$`)
)

// Manifest identifies a generated project and its registered API packages.
type Manifest struct {
	Module           string         `json:"module"`
	FrameworkVersion string         `json:"framework_version"`
	TemplateVersion  int            `json:"template_version"`
	APIs             []GeneratedAPI `json:"apis"`
}

// GeneratedAPI records one API package registered in ModulesPath.
type GeneratedAPI struct {
	Name       string `json:"name"`
	Package    string `json:"package"`
	Path       string `json:"path"`
	ModuleType string `json:"module_type"`
	// Files maps generator-owned file paths (project-relative slash paths)
	// to their sha256 digests at generation time. It exists only on
	// template v2 manifests; v1 entries leave it empty. User-modified files
	// (notably service.go) are never auto-overwritten on upgrade.
	Files map[string]string `json:"files,omitempty"`
}

func NewManifest(modulePath, frameworkVersion string) Manifest {
	return Manifest{
		Module:           modulePath,
		FrameworkVersion: frameworkVersion,
		TemplateVersion:  TemplateVersion,
		APIs:             []GeneratedAPI{},
	}
}

func ReadManifest(root string) (Manifest, error) {
	path := filepath.Join(root, filepath.FromSlash(ManifestPath))
	contents, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, fmt.Errorf("read scaffold manifest %q: %w", path, err)
	}
	versionDecoder := json.NewDecoder(strings.NewReader(string(contents)))
	var version struct {
		TemplateVersion int `json:"template_version"`
	}
	// Version first, explicitly: v1 and v2 decode strictly against their own
	// shape, anything else is rejected without guessing.
	if err := versionDecoder.Decode(&version); err != nil {
		return Manifest{}, fmt.Errorf("decode scaffold manifest %q: %w", path, err)
	}
	if version.TemplateVersion != TemplateVersion && version.TemplateVersion != TemplateVersionFiles {
		return Manifest{}, fmt.Errorf("decode scaffold manifest %q: unsupported template version %d (supported: 1, 2)", path, version.TemplateVersion)
	}
	var manifest Manifest
	decoder := json.NewDecoder(strings.NewReader(string(contents)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode scaffold manifest %q: %w", path, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return Manifest{}, fmt.Errorf("decode scaffold manifest %q: trailing content: %w", path, err)
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, fmt.Errorf("validate scaffold manifest %q: %w", path, err)
	}
	return manifest, nil
}

func MarshalManifest(manifest Manifest) ([]byte, error) {
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	manifest.APIs = append([]GeneratedAPI(nil), manifest.APIs...)
	sort.Slice(manifest.APIs, func(i, j int) bool {
		if manifest.APIs[i].Package == manifest.APIs[j].Package {
			return manifest.APIs[i].Name < manifest.APIs[j].Name
		}
		return manifest.APIs[i].Package < manifest.APIs[j].Package
	})
	contents, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode scaffold manifest: %w", err)
	}
	return append(contents, '\n'), nil
}

func WriteManifest(root string, manifest Manifest) error {
	contents, err := MarshalManifest(manifest)
	if err != nil {
		return err
	}
	path := filepath.Join(root, filepath.FromSlash(ManifestPath))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create scaffold metadata directory: %w", err)
	}
	if err := os.WriteFile(path, contents, 0o644); err != nil {
		return fmt.Errorf("write scaffold manifest %q: %w", path, err)
	}
	return nil
}

func (manifest Manifest) Validate() error {
	if err := module.CheckPath(manifest.Module); err != nil {
		return fmt.Errorf("invalid project module %q: %w", manifest.Module, err)
	}
	if err := module.Check(frameworkModule, manifest.FrameworkVersion); err != nil {
		return fmt.Errorf("invalid framework version %q: %w", manifest.FrameworkVersion, err)
	}
	if manifest.TemplateVersion != TemplateVersion && manifest.TemplateVersion != TemplateVersionFiles {
		return fmt.Errorf("unsupported template version %d", manifest.TemplateVersion)
	}

	seenPackages := make(map[string]struct{}, len(manifest.APIs))
	for index, api := range manifest.APIs {
		if strings.TrimSpace(api.Name) == "" || strings.ContainsAny(api.Name, "\r\n\x00") {
			return fmt.Errorf("api %d has invalid name", index)
		}
		if !generatedPackageName.MatchString(api.Package) {
			return fmt.Errorf("api %d has invalid package %q", index, api.Package)
		}
		if api.Package == "app" {
			return fmt.Errorf("api %d package must not be app", index)
		}
		if _, exists := seenPackages[api.Package]; exists {
			return fmt.Errorf("duplicate generated API package %q", api.Package)
		}
		seenPackages[api.Package] = struct{}{}
		if err := validateManagedPath(api.Path); err != nil {
			return fmt.Errorf("api %d path: %w", index, err)
		}
		if expected := "internal/" + api.Package; api.Path != expected {
			return fmt.Errorf("api %d path must be %q", index, expected)
		}
		if !generatedModuleType.MatchString(api.ModuleType) {
			return fmt.Errorf("api %d has invalid module type %q", index, api.ModuleType)
		}
		if expected := api.Package + ".Module"; api.ModuleType != expected {
			return fmt.Errorf("api %d module type must be %q", index, expected)
		}
		for file, digest := range api.Files {
			if err := validateManagedPath(file); err != nil {
				return fmt.Errorf("api %d file: %w", index, err)
			}
			if len(digest) != 64 || strings.Trim(digest, "0123456789abcdef") != "" {
				return fmt.Errorf("api %d file %q has an invalid sha256 digest", index, file)
			}
		}
	}

	return nil
}

func validateManagedPath(path string) error {
	if path == "" || filepath.IsAbs(path) || filepath.ToSlash(filepath.Clean(path)) != path || path == "." || strings.HasPrefix(path, "../") {
		return fmt.Errorf("path %q must be a clean relative slash path", path)
	}
	return nil
}

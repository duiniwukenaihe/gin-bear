package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/duiniwukenaihe/gin-bear/internal/scaffold"
	"github.com/spf13/cobra"
)

// applyPlanFile executes a previously previewed plan. The stored plan is a
// gate, not an instruction: inputs, project root, and every content digest
// must match a freshly computed plan, otherwise the plan is stale and
// rejected. No path inside the plan file is trusted.
func applyPlanFile(cmd *cobra.Command, directory, planPath string) error {
	contents, err := os.ReadFile(planPath)
	if err != nil {
		return fmt.Errorf("read plan %s: %w", planPath, err)
	}
	var document previewDocument
	decoder := json.NewDecoder(strings.NewReader(string(contents)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return fmt.Errorf("decode plan %s: %w", planPath, err)
	}
	if document.SchemaVersion != previewSchemaVersion {
		return fmt.Errorf("plan %s has schema version %d, want %d (re-run --dry-run with this CLI)", planPath, document.SchemaVersion, previewSchemaVersion)
	}
	if !document.DryRun || document.Plan == nil {
		return fmt.Errorf("plan %s is not a dry-run preview document", planPath)
	}
	stored := document.Plan
	if stored.Kind != "api" && stored.Kind != "model" && stored.Kind != "dto" {
		return fmt.Errorf("plan %s has unsupported kind %q", planPath, stored.Kind)
	}
	if stored.ProjectRoot != directory {
		return fmt.Errorf("plan %s targets %s, not this project (%s): refusing a cross-project apply", planPath, stored.ProjectRoot, directory)
	}
	expectedTarget := filepath.ToSlash(filepath.Join("internal", packageName(stored.Name)))
	if stored.Target != expectedTarget || packageName(stored.Name) == "resource" && len(nameParts(stored.Name)) == 0 {
		return fmt.Errorf("plan %s names an invalid target %q", planPath, stored.Target)
	}
	if err := validatePlanPaths(directory, stored); err != nil {
		return fmt.Errorf("plan %s: %w", planPath, err)
	}

	activePackage := packageName(stored.Name)
	var managed *managedGeneration
	if stored.Kind == "api" {
		locked, err := prepareManagedGeneration(directory, stored.Kind, activePackage)
		if err != nil {
			return fmt.Errorf("plan %s is stale: %w", planPath, err)
		}
		managed = locked
		defer managed.release()
	}

	var database generatedAPIDatabase
	opts := resourceOptions{
		Kind:      stored.Kind,
		Name:      stored.Name,
		Fields:    stored.Fields,
		Directory: directory,
		database:  &database,
	}
	if stored.Kind == "api" {
		// Replay the preview's selection rule: explicit inputs verbatim
		// (validated below), otherwise default discovery. A different rule
		// would resolve different sources and fail the equality check.
		for _, input := range stored.ConfigInputs {
			if input == "" || filepath.IsAbs(input) || filepath.ToSlash(filepath.Clean(input)) != filepath.ToSlash(input) {
				return fmt.Errorf("plan %s has invalid config input %q", planPath, input)
			}
			if input == "." || input == ".." || strings.HasPrefix(filepath.ToSlash(input), "../") || strings.Contains(filepath.ToSlash(input), "/../") {
				return fmt.Errorf("plan %s config input %q escapes the project", planPath, input)
			}
		}
		opts.ConfigPaths = append([]string(nil), stored.ConfigInputs...)
		resolved, err := resolveGeneratedAPIDatabase(directory, opts.ConfigPaths)
		if err != nil {
			return err
		}
		database = resolved
		opts.database = &database
	}
	fresh, err := planResource(cmd.Context(), opts, database, managed)
	if err != nil {
		return fmt.Errorf("plan %s is stale: %w", planPath, err)
	}
	if len(fresh.Conflicts) > 0 {
		return fmt.Errorf("plan %s is stale: %s", planPath, fresh.Conflicts[0])
	}
	if diff := plansEqual(stored, fresh); diff != "" {
		return fmt.Errorf("plan %s is stale: %s (re-run --dry-run)", planPath, diff)
	}

	result, err := executePlan(cmd.Context(), directory, fresh, managed)
	if err != nil {
		return err
	}
	if managed != nil {
		if err := upgradeManifestToV2(directory, activePackage, planFileSHAs(fresh)); err != nil {
			return fmt.Errorf("upgrade manifest: %w", err)
		}
	}
	if result.AdapterHint != "" {
		fmt.Fprintf(cmd.ErrOrStderr(), "Warning: %s\n", result.AdapterHint)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Applied %s from %s\n", result.Path, planPath)
	return nil
}

// validatePlanPaths rejects absolute paths, dot-dot escapes, and symlink
// escapes through existing parents. Every planned write must stay inside root.
func validatePlanPaths(root string, plan *resourcePlan) error {
	paths := []string{plan.Target}
	for _, file := range plan.Files {
		paths = append(paths, file.Rel)
	}
	if plan.Migration != nil {
		paths = append(paths, plan.Migration.Files...)
	}
	if plan.Manifest != nil {
		paths = append(paths, plan.Manifest.Path, plan.Manifest.Registry)
	}
	for _, rel := range paths {
		if rel == "" || filepath.IsAbs(rel) || filepath.ToSlash(filepath.Clean(rel)) != rel || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") || strings.Contains(rel, "/../") {
			return fmt.Errorf("path %q escapes the project", rel)
		}
		joined := filepath.Join(root, filepath.FromSlash(rel))
		if outside, err := pathOutsideRoot(root, joined); err != nil {
			return err
		} else if outside {
			return fmt.Errorf("path %q escapes the project", rel)
		}
		if err := checkParentSymlinks(root, joined); err != nil {
			return err
		}
	}
	return nil
}

func pathOutsideRoot(root, joined string) (bool, error) {
	cleanRoot, err := filepath.Abs(root)
	if err != nil {
		return false, err
	}
	cleanJoined, err := filepath.Abs(joined)
	if err != nil {
		return false, err
	}
	relative, err := filepath.Rel(cleanRoot, cleanJoined)
	if err != nil {
		return false, err
	}
	return relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)), nil
}

// checkParentSymlinks rejects writes through existing symlinked parents that
// resolve outside the project (e.g. internal/ -> /tmp/evil).
func checkParentSymlinks(root, joined string) error {
	cleanRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	dir := filepath.Dir(joined)
	for dir != root && strings.HasPrefix(dir, root) {
		info, err := os.Lstat(dir)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				dir = filepath.Dir(dir)
				continue
			}
			return fmt.Errorf("inspect %q: %w", dir, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			resolved, err := filepath.EvalSymlinks(dir)
			if err != nil {
				return fmt.Errorf("resolve symlink %q: %w", dir, err)
			}
			if outside, err := pathOutsideRoot(cleanRoot, resolved); err != nil || outside {
				return fmt.Errorf("path escapes the project through symlink %q", dir)
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return nil
}

// plansEqual reports the first difference between a stored plan and a fresh
// one, or "" when the stored plan is still current.
func plansEqual(stored, fresh *resourcePlan) string {
	if stored.Kind != fresh.Kind || stored.Name != fresh.Name || stored.Fields != fresh.Fields {
		return "inputs changed"
	}
	if stored.Target != fresh.Target || stored.Framework != fresh.Framework || stored.Template != fresh.Template {
		return "target or toolchain changed"
	}
	if strings.Join(stored.Config, "\x00") != strings.Join(fresh.Config, "\x00") {
		return "configuration sources changed"
	}
	if strings.Join(stored.ConfigInputs, "\x00") != strings.Join(fresh.ConfigInputs, "\x00") {
		return "configuration inputs changed"
	}
	if len(stored.Files) != len(fresh.Files) {
		return "rendered file set changed"
	}
	for i := range stored.Files {
		a, b := stored.Files[i], fresh.Files[i]
		if a.Rel != b.Rel || a.SHA256 != b.SHA256 || a.Bytes != b.Bytes || a.Action != b.Action {
			return fmt.Sprintf("file %s changed", a.Rel)
		}
	}
	if (stored.Migration == nil) != (fresh.Migration == nil) {
		return "migration selection changed"
	}
	if stored.Migration != nil {
		a, b := stored.Migration, fresh.Migration
		if a.Version != b.Version || a.Dialect != b.Dialect || a.Up != b.Up || a.Down != b.Down || strings.Join(a.Files, "\x00") != strings.Join(b.Files, "\x00") {
			return "migration changed"
		}
	}
	if (stored.Manifest == nil) != (fresh.Manifest == nil) {
		return "manifest selection changed"
	}
	if stored.Manifest != nil {
		if !reflect.DeepEqual(stored.Manifest.Added, fresh.Manifest.Added) {
			return "manifest entry changed"
		}
		if stored.Manifest.ContentSHA != fresh.Manifest.ContentSHA || stored.Manifest.RegistrySH != fresh.Manifest.RegistrySH {
			return "manifest contents changed"
		}
	}
	if len(stored.GoMod) != len(fresh.GoMod) {
		return "go.mod pins changed"
	}
	for i := range stored.GoMod {
		if stored.GoMod[i] != fresh.GoMod[i] {
			return "go.mod pins changed"
		}
	}
	if strings.Join(stored.Conflicts, "\x00") != strings.Join(fresh.Conflicts, "\x00") {
		return "conflicts changed"
	}
	return ""
}

// upgradeManifestToV2 moves a manifest to the digest-carrying version after a
// previewed apply, recording generator-owned file digests for the new entry.
// History and entries are otherwise untouched.
func upgradeManifestToV2(directory, activePackage string, shas map[string]string) error {
	manifest, err := scaffold.ReadManifest(directory)
	if err != nil {
		return err
	}
	manifest.TemplateVersion = scaffold.TemplateVersionFiles
	for i := range manifest.APIs {
		if manifest.APIs[i].Package == activePackage {
			copied := make(map[string]string, len(shas))
			for path, digest := range shas {
				copied[path] = digest
			}
			manifest.APIs[i].Files = copied
		}
	}
	return scaffold.WriteManifest(directory, manifest)
}

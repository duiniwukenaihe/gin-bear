package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/duiniwukenaihe/gin-bear/internal/scaffold"
	"github.com/duiniwukenaihe/gin-bear/pkg/bear"
	"github.com/spf13/cobra"
	"golang.org/x/mod/modfile"
	"golang.org/x/mod/semver"
)

// Generation preview schema version. Bump only with an incompatible change.
const previewSchemaVersion = 1

// plannedFile is one rendered resource file with a project-relative slash path.
type plannedFile struct {
	Rel      string `json:"path"`
	Name     string `json:"-"`
	Action   string `json:"action"`
	Bytes    int64  `json:"bytes"`
	SHA256   string `json:"sha256"`
	Contents []byte `json:"-"`
}

// plannedMigration is the migration pair a generation would publish.
type plannedMigration struct {
	Version string   `json:"version"`
	Name    string   `json:"name"`
	Dialect string   `json:"dialect"`
	Files   []string `json:"files"`
	Up      string   `json:"up"`
	Down    string   `json:"down"`
}

// plannedManifest describes the manifest/registry update a generation would write.
type plannedManifest struct {
	Path       string                `json:"path"`
	Registry   string                `json:"registry"`
	Action     string                `json:"action"`
	Added      scaffold.GeneratedAPI `json:"added"`
	ContentSHA string                `json:"manifest_sha256"`
	RegistrySH string                `json:"registry_sha256"`
}

// modRequire is one go.mod requirement a generation would pin.
type modRequire struct {
	Path    string `json:"path"`
	Version string `json:"version"`
}

// resourcePlan is the fully computed outcome of one generation. Real
// generation and --dry-run preview share it, so both observe identical bytes.
type resourcePlan struct {
	Kind        string   `json:"kind"`
	Name        string   `json:"name"`
	Fields      string   `json:"fields"`
	ProjectRoot string   `json:"project_root"`
	Config      []string `json:"config_sources"`
	// ConfigInputs records the explicit --config arguments in the canonical
	// project-relative form apply replays. Empty means default discovery.
	// Absolute paths inside the project are relativized at preview time;
	// paths outside the project are refused there, so a preview always
	// produces a plan apply can accept.
	ConfigInputs []string          `json:"config_inputs"`
	Framework    string            `json:"framework_version"`
	Template     int               `json:"template_version"`
	Target       string            `json:"target"`
	Files        []plannedFile     `json:"files"`
	Migration    *plannedMigration `json:"migration,omitempty"`
	Manifest     *plannedManifest  `json:"manifest,omitempty"`
	GoMod        []modRequire      `json:"go_mod_adds"`
	Warnings     []string          `json:"warnings"`
	Conflicts    []string          `json:"conflicts"`

	data     resourceData
	fields   []field
	database generatedAPIDatabase
	managed  bool
	// pendingManifest holds the manifest the update is computed from; the
	// public Manifest section is filled after files render so v2 digests
	// cover the actual bytes.
	pendingManifest *scaffold.Manifest
	migration       resourceMigration
	dialect         string
}

// planResource computes a generation without writing anything: no resource
// directory, no lock file, no manifest, no migration, no go.mod change.
// Hard failures (bad fields, unreadable config, unknown kind) are errors;
// pre-existing targets are reported as conflicts for the caller to decide.
func planResource(ctx context.Context, opts resourceOptions, database generatedAPIDatabase, locked *managedGeneration) (*resourcePlan, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if opts.Directory == "" {
		return nil, errors.New("project directory is required")
	}
	activePackage := packageName(opts.Name)
	if activePackage == "resource" && len(nameParts(opts.Name)) == 0 {
		return nil, fmt.Errorf("resource name %q is invalid", opts.Name)
	}
	fields, err := parseResourceFields(opts.Fields)
	if err != nil {
		return nil, err
	}
	data := resourceData{
		PackageName: activePackage,
		Title:       titleName(opts.Name),
		RouteName:   routeName(opts.Name),
		Fields:      fields,
		Imports:     resourceImports(fields),
	}
	plan := &resourcePlan{
		Kind:        opts.Kind,
		Name:        opts.Name,
		Fields:      opts.Fields,
		ProjectRoot: opts.Directory,
		Framework:   bear.Version,
		Template:    scaffold.TemplateVersion,
		Target:      filepath.ToSlash(filepath.Join("internal", activePackage)),
		data:        data,
		fields:      fields,
		database:    database,
	}
	plan.Config = generationConfigPlanSources(opts.Directory, opts.ConfigPaths)
	canonical, err := canonicalConfigInputs(opts.Directory, opts.ConfigPaths)
	if err != nil {
		return nil, err
	}
	plan.ConfigInputs = canonical

	manifest, managed, err := planManifest(ctx, opts, activePackage, locked)
	if err != nil {
		return nil, err
	}
	plan.managed = managed
	plan.pendingManifest = manifest

	templates, err := templatesForKind(opts.Kind)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(templates))
	for filename := range templates {
		names = append(names, filename)
	}
	sort.Strings(names)
	for _, filename := range names {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		contents, err := executeResourceTemplate(filename, templates[filename], data)
		if err != nil {
			return nil, err
		}
		formatted, err := format.Source(contents)
		if err != nil {
			return nil, fmt.Errorf("format generated file %q: %w", filename, err)
		}
		rel := filepath.ToSlash(filepath.Join("internal", activePackage, filename))
		plan.Files = append(plan.Files, plannedFile{
			Rel:      rel,
			Name:     filename,
			Action:   "create",
			Bytes:    int64(len(formatted)),
			SHA256:   hexDigest(formatted),
			Contents: formatted,
		})
	}

	target := filepath.Join(opts.Directory, "internal", activePackage)
	if _, err := os.Lstat(target); err == nil {
		plan.Conflicts = append(plan.Conflicts, fmt.Sprintf("resource package %q already exists", target))
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect resource package %q: %w", target, err)
	}

	if plan.managed && plan.pendingManifest != nil {
		entry, manifestBytes, registryBytes, err := renderManifestUpdate(*plan.pendingManifest, data, planFileSHAs(plan))
		if err != nil {
			return nil, err
		}
		plan.Manifest = &plannedManifest{
			Path:       scaffold.ManifestPath,
			Registry:   scaffold.ModulesPath,
			Action:     "update",
			Added:      entry,
			ContentSHA: hexDigest(manifestBytes),
			RegistrySH: hexDigest(registryBytes),
		}
	}
	plan.pendingManifest = nil

	pins, err := computePins(opts.Directory, opts.Kind, fields)
	if err != nil {
		return nil, err
	}
	plan.GoMod = pins
	if opts.Kind == "api" {
		if err := planMigration(data, database, plan); err != nil {
			return nil, err
		}
		if hint := adapterHintForDatabase(opts.Directory, database); hint != "" {
			plan.Warnings = append(plan.Warnings, hint)
		}
	}
	return plan, nil
}

// planManifest resolves the manifest without locking: the locked generation
// reuses its held manifest, preview reads the current one.
func planManifest(_ context.Context, opts resourceOptions, activePackage string, locked *managedGeneration) (*scaffold.Manifest, bool, error) {
	if opts.Kind != "api" {
		return nil, false, nil
	}
	if locked != nil {
		manifest := locked.manifest
		return &manifest, true, nil
	}
	exists, err := scaffoldManifestExists(opts.Directory)
	if err != nil || !exists {
		return nil, false, err
	}
	manifest, err := scaffold.ReadManifest(opts.Directory)
	if err != nil {
		return nil, false, err
	}
	return &manifest, true, nil
}

// generationConfigPlanSources lists the configuration files a plan was
// computed from, using the same selection as generation itself.
func generationConfigPlanSources(dir string, configPaths []string) []string {
	sources, err := bear.GenerationConfigPaths(dir, configPaths...)
	if err != nil {
		return nil
	}
	return sources
}

// canonicalConfigInputs normalizes explicit --config arguments to the
// project-relative slash form apply replays. Absolute paths inside the
// project become relative; any input resolving outside the project is
// refused at preview time, so preview never produces a plan apply must
// reject. Direct generation still accepts outside absolute paths; only the
// preview/apply contract is closed.
func canonicalConfigInputs(directory string, configPaths []string) ([]string, error) {
	if len(configPaths) == 0 {
		return nil, nil
	}
	root, err := filepath.Abs(directory)
	if err != nil {
		return nil, fmt.Errorf("resolve project directory: %w", err)
	}
	canonical := make([]string, 0, len(configPaths))
	for _, input := range configPaths {
		if input == "" {
			return nil, fmt.Errorf("generation config path is empty")
		}
		candidate := input
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(root, candidate)
		}
		relative, err := filepath.Rel(root, candidate)
		if err != nil {
			return nil, fmt.Errorf("config %q cannot be made project-relative: %w", input, err)
		}
		if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("config %q is outside the project; preview/apply only accept project-relative --config paths", input)
		}
		canonical = append(canonical, filepath.ToSlash(relative))
	}
	return canonical, nil
}

// planMigration renders the migration pair and records pre-existing files as
// conflicts instead of failing, so preview stays advisory.
func planMigration(data resourceData, database generatedAPIDatabase, plan *resourcePlan) error {
	if !database.Enabled {
		return nil
	}
	migration, err := resourceMigrationFor(plan.ProjectRoot, data, database.Dialect)
	if err != nil {
		return fmt.Errorf("plan resource migration: %w", err)
	}
	plan.migration = migration
	plan.dialect = database.Dialect
	files := []string{
		filepath.ToSlash(filepath.Join(migrationDirectory, fmt.Sprintf("%s_%s.up.sql", migration.Version, migration.Name))),
		filepath.ToSlash(filepath.Join(migrationDirectory, fmt.Sprintf("%s_%s.down.sql", migration.Version, migration.Name))),
	}
	for _, relative := range files {
		if _, err := os.Lstat(filepath.Join(plan.ProjectRoot, filepath.FromSlash(relative))); err == nil {
			plan.Conflicts = append(plan.Conflicts, fmt.Sprintf("migration %q already exists", relative))
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect migration %q: %w", relative, err)
		}
	}
	plan.Migration = &plannedMigration{
		Version: migration.Version,
		Name:    migration.Name,
		Dialect: database.Dialect,
		Files:   files,
		Up:      migration.UpSQL,
		Down:    migration.DownSQL,
	}
	return nil
}

// renderManifestUpdate computes the manifest and registry bytes for one more
// API entry without writing. It rejects duplicate packages exactly like the
// locked registration path. On template v2 manifests it attaches the
// generator-owned file digests; v1 output stays byte-identical to before.
func renderManifestUpdate(manifest scaffold.Manifest, data resourceData, files map[string]string) (scaffold.GeneratedAPI, []byte, []byte, error) {
	for _, api := range manifest.APIs {
		if api.Package == data.PackageName {
			return scaffold.GeneratedAPI{}, nil, nil, fmt.Errorf("generated API package %q is already registered", data.PackageName)
		}
	}
	entry := scaffold.GeneratedAPI{
		Name:       data.Title,
		Package:    data.PackageName,
		Path:       filepath.ToSlash(filepath.Join("internal", data.PackageName)),
		ModuleType: data.PackageName + ".Module",
	}
	if manifest.TemplateVersion == scaffold.TemplateVersionFiles {
		copied := make(map[string]string, len(files))
		for path, digest := range files {
			copied[path] = digest
		}
		entry.Files = copied
	}
	// Copy before appending: the input manifest may be shared with a held
	// generation lock or a preview snapshot.
	apis := append([]scaffold.GeneratedAPI(nil), manifest.APIs...)
	apis = append(apis, entry)
	manifest.APIs = apis
	sort.SliceStable(manifest.APIs, func(i, j int) bool {
		if manifest.APIs[i].Package == manifest.APIs[j].Package {
			return manifest.APIs[i].Name < manifest.APIs[j].Name
		}
		return manifest.APIs[i].Package < manifest.APIs[j].Package
	})
	manifestContents, err := scaffold.MarshalManifest(manifest)
	if err != nil {
		return scaffold.GeneratedAPI{}, nil, nil, fmt.Errorf("render scaffold manifest: %w", err)
	}
	registryContents, err := renderModuleRegistry(manifest)
	if err != nil {
		return scaffold.GeneratedAPI{}, nil, nil, err
	}
	return entry, manifestContents, registryContents, nil
}

// planFileSHAs indexes planned file contents by project-relative path.
func planFileSHAs(plan *resourcePlan) map[string]string {
	shas := make(map[string]string, len(plan.Files))
	for _, file := range plan.Files {
		shas[file.Rel] = file.SHA256
	}
	return shas
}

func hexDigest(contents []byte) string {
	sum := sha256.Sum256(contents)
	return hex.EncodeToString(sum[:])
}

// computePins lists the go.mod requirements generation would pin, without
// writing. applyPins writes a previously computed list.
func computePins(directory, kind string, fields []field) ([]modRequire, error) {
	needsDecimal := false
	for _, item := range fields {
		if item.GoType == "decimal.Decimal" {
			needsDecimal = true
			break
		}
	}
	type requirement struct {
		path             string
		version          string
		preserveExisting bool
	}
	requirements := make([]requirement, 0, 3)
	if kind == "api" {
		requirements = append(requirements,
			requirement{path: "github.com/gin-gonic/gin", version: ginModuleVersion},
			requirement{path: "gorm.io/gorm", version: gormModuleVersion},
		)
	}
	if needsDecimal {
		requirements = append(requirements, requirement{
			path:             "github.com/shopspring/decimal",
			version:          decimalModuleVersion,
			preserveExisting: true,
		})
	}
	if len(requirements) == 0 {
		return nil, nil
	}
	path := filepath.Join(directory, "go.mod")
	contents, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read go.mod: %w", err)
	}
	file, err := modfile.Parse(path, contents, nil)
	if err != nil {
		return nil, fmt.Errorf("parse go.mod: %w", err)
	}
	var pins []modRequire
	for _, requirement := range requirements {
		if requirement.preserveExisting && hasRequirement(file, requirement.path) {
			continue
		}
		target := requirement.version
		for _, existing := range file.Require {
			if existing.Mod.Path != requirement.path {
				continue
			}
			if semver.Compare(existing.Mod.Version, requirement.version) >= 0 {
				target = existing.Mod.Version
			}
		}
		pins = append(pins, modRequire{Path: requirement.path, Version: target})
	}
	return pins, nil
}

func applyPins(directory string, pins []modRequire) error {
	if len(pins) == 0 {
		return nil
	}
	path := filepath.Join(directory, "go.mod")
	contents, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read go.mod: %w", err)
	}
	file, err := modfile.Parse(path, contents, nil)
	if err != nil {
		return fmt.Errorf("parse go.mod: %w", err)
	}
	changed := false
	for _, pin := range pins {
		requirementChanged, err := ensureDirectRequirement(file, pin.Path, pin.Version)
		if err != nil {
			return err
		}
		changed = changed || requirementChanged
	}
	if !changed {
		return nil
	}
	formatted, err := file.Format()
	if err != nil {
		return fmt.Errorf("format go.mod: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("inspect go.mod: %w", err)
	}
	if err := os.WriteFile(path, formatted, info.Mode().Perm()); err != nil {
		return fmt.Errorf("write go.mod: %w", err)
	}
	return nil
}

// previewDocument is the single machine-readable preview document.
type previewDocument struct {
	SchemaVersion int           `json:"schema_version"`
	DryRun        bool          `json:"dry_run"`
	Plan          *resourcePlan `json:"plan"`
}

// previewResource computes the shared plan and reports it without writing
// anything: no resource directory, no lock file, no manifest, no migration,
// no go.mod change.
func previewResource(cmd *cobra.Command, opts resourceOptions, format string) error {
	snapshot := generatedAPIDatabase{}
	if opts.database != nil {
		snapshot = *opts.database
	}
	plan, err := planResource(cmd.Context(), opts, snapshot, nil)
	if err != nil {
		return err
	}
	if format == "json" {
		encoded, err := json.MarshalIndent(previewDocument{
			SchemaVersion: previewSchemaVersion,
			DryRun:        true,
			Plan:          plan,
		}, "", "  ")
		if err != nil {
			return err
		}
		// Stdout carries exactly one document; warnings go to stderr.
		fmt.Fprintf(cmd.OutOrStdout(), "%s\n", encoded)
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "preview %s %s -> %s\n", plan.Kind, plan.Name, plan.Target)
		fmt.Fprintf(cmd.OutOrStdout(), "config: %s\n", strings.Join(plan.Config, ", "))
		for _, file := range plan.Files {
			fmt.Fprintf(cmd.OutOrStdout(), "  create %s (%d bytes, sha256:%s)\n", file.Rel, file.Bytes, file.SHA256[:12])
		}
		if plan.Migration != nil {
			fmt.Fprintf(cmd.OutOrStdout(), "  migration %s (%s): %s\n", plan.Migration.Version, plan.Migration.Dialect, strings.Join(plan.Migration.Files, ", "))
		} else {
			fmt.Fprintf(cmd.OutOrStdout(), "  migration: none (database disabled)\n")
		}
		if plan.Manifest != nil {
			fmt.Fprintf(cmd.OutOrStdout(), "  manifest: update %s (add package %s)\n", plan.Manifest.Path, plan.Manifest.Added.Package)
		}
		for _, pin := range plan.GoMod {
			fmt.Fprintf(cmd.OutOrStdout(), "  go.mod: require %s %s\n", pin.Path, pin.Version)
		}
		for _, conflict := range plan.Conflicts {
			fmt.Fprintf(cmd.OutOrStdout(), "  conflict: %s\n", conflict)
		}
	}
	for _, warning := range plan.Warnings {
		fmt.Fprintf(cmd.ErrOrStderr(), "Warning: %s\n", warning)
	}
	return nil
}

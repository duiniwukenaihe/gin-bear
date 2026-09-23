package cli

import (
	"context"
	"errors"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"
	"time"
	"unicode"

	"github.com/duiniwukenaihe/gin-bear/internal/atomicdir"
	"github.com/duiniwukenaihe/gin-bear/internal/scaffold"
	"github.com/spf13/cobra"
	"golang.org/x/mod/modfile"
	"golang.org/x/mod/semver"
)

const (
	decimalModuleVersion = "v1.4.0"
	ginModuleVersion     = "v1.12.0"
	gormModuleVersion    = "v1.26.0"
	// scaffoldConfigFile is the configuration file `bear new` writes and the
	// generated server loads by default.
	scaffoldConfigFile = "application.yaml"
)

type resourceOptions struct {
	Kind      string
	Name      string
	Fields    string
	Directory string
	// ConfigPaths replaces the default generation file chain, in order.
	// Relative paths resolve against Directory (the go.mod project root).
	ConfigPaths []string
	// database carries a pre-resolved snapshot so warnings, dialect selection
	// and SQL writing share a single parse per generation.
	database *generatedAPIDatabase
}

// generateResult is the published outcome of one generation: the resource
// path for stdout plus the adapter hint for stderr, both derived from the
// same database snapshot.
type generateResult struct {
	Path        string
	AdapterHint string
}

type field struct {
	Name           string
	GoType         string
	JSONName       string
	Validate       string
	UpdateValidate string
	GormTag        string
}

type resourceData struct {
	PackageName string
	Title       string
	RouteName   string
	Fields      []field
	Imports     string
}

func genCommand() *cobra.Command {
	var fields string
	var configPaths []string
	var dryRun bool
	var previewFormat string
	var planFile string
	command := &cobra.Command{
		Use:   "gen <type> <name> | gen apply --plan <file>",
		Short: "Generate code (api|model|dto) or apply a previewed plan",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 && args[0] == "apply" {
				plan, err := cmd.Flags().GetString("plan")
				if err != nil || strings.TrimSpace(plan) == "" {
					return errors.New("gen apply requires --plan <file>")
				}
				return nil
			}
			if len(args) == 2 {
				return nil
			}
			return errors.New("gen requires <type> <name> or \"apply --plan <file>\"")
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 && args[0] == "apply" {
				if fields != "" || len(configPaths) > 0 || dryRun || previewFormat != "text" {
					return errors.New("gen apply accepts only --plan <file>")
				}
				directory, err := genProjectDirectory()
				if err != nil {
					return err
				}
				return applyPlanFile(cmd, directory, planFile)
			}
			kind := strings.ToLower(args[0])
			if kind != "api" && kind != "model" && kind != "dto" {
				return fmt.Errorf("unsupported generation type %q (supported: api, model, dto)", args[0])
			}
			if kind != "api" && len(configPaths) > 0 {
				return fmt.Errorf("--config is only supported for \"gen api\" (kind %q reads no database configuration)", kind)
			}
			if previewFormat != "text" && previewFormat != "json" {
				return fmt.Errorf("invalid --format %q (supported: text, json)", previewFormat)
			}
			if previewFormat != "text" && !dryRun {
				return fmt.Errorf("--format is only supported with --dry-run")
			}
			directory, err := genProjectDirectory()
			if err != nil {
				return err
			}
			managedProject := false
			if kind == "api" {
				managedProject, err = scaffoldManifestExists(directory)
				if err != nil {
					return err
				}
			}
			// Resolve the database contract once before generating so an
			// unreadable configuration fails before any file is written, and
			// warnings, dialect selection and SQL writing share one snapshot.
			var database *generatedAPIDatabase
			if kind == "api" {
				resolved, err := resolveGeneratedAPIDatabase(directory, configPaths)
				if err != nil {
					return err
				}
				database = &resolved
			}
			if dryRun {
				return previewResource(cmd, resourceOptions{
					Kind:        kind,
					Name:        args[1],
					Fields:      fields,
					Directory:   directory,
					ConfigPaths: configPaths,
					database:    database,
				}, previewFormat)
			}
			result, err := generateResource(cmd.Context(), resourceOptions{
				Kind:        kind,
				Name:        args[1],
				Fields:      fields,
				Directory:   directory,
				ConfigPaths: configPaths,
				database:    database,
			})
			if err != nil {
				return err
			}
			// Warnings go to stderr so the machine-readable stdout stays stable.
			if result.AdapterHint != "" {
				fmt.Fprintf(cmd.ErrOrStderr(), "Warning: %s\n", result.AdapterHint)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Generated %s\n", result.Path)
			if kind == "api" && !managedProject {
				fmt.Fprintf(cmd.OutOrStdout(), "No %s found; register %s.NewModule() manually with application.AddModule or application.AddModuleE.\n", scaffold.ManifestPath, packageName(args[1]))
			}
			return nil
		},
	}
	command.Flags().StringVarP(&fields, "fields", "f", "", "fields as name:type pairs")
	command.Flags().StringArrayVar(&configPaths, "config", nil, "generation config file (repeatable, relative to project root; only for gen api)")
	command.Flags().BoolVar(&dryRun, "dry-run", false, "preview the generation without writing anything")
	command.Flags().StringVar(&previewFormat, "format", "text", "preview output format with --dry-run: text or json")
	command.Flags().StringVar(&planFile, "plan", "", "preview file for \"gen apply --plan <file>\"")
	return command
}

// genProjectDirectory resolves the go.mod project root from the working directory.
func genProjectDirectory() (string, error) {
	currentDirectory, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve project directory: %w", err)
	}
	return nearestGoModRoot(currentDirectory)
}

func nearestGoModRoot(start string) (string, error) {
	start, err := filepath.Abs(start)
	if err != nil {
		return "", fmt.Errorf("resolve generation directory: %w", err)
	}
	start = filepath.Clean(start)
	for directory := start; ; directory = filepath.Dir(directory) {
		goModPath := filepath.Join(directory, "go.mod")
		info, statErr := os.Stat(goModPath)
		if statErr == nil {
			if info.IsDir() {
				return "", fmt.Errorf("project module file %q is a directory", goModPath)
			}
			return directory, nil
		}
		if !errors.Is(statErr, os.ErrNotExist) {
			return "", fmt.Errorf("inspect project module file %q: %w", goModPath, statErr)
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return start, nil
		}
	}
}

func generateResource(ctx context.Context, opts resourceOptions) (generateResult, error) {
	if err := ctx.Err(); err != nil {
		return generateResult{}, err
	}
	if opts.Directory == "" {
		return generateResult{}, errors.New("project directory is required")
	}
	if opts.Kind != "api" && len(opts.ConfigPaths) > 0 {
		return generateResult{}, fmt.Errorf("--config is only supported for \"gen api\" (kind %q reads no database configuration)", opts.Kind)
	}
	// Single database selection per generation: warnings, dialect choice and
	// SQL writing share this snapshot. A pre-resolved snapshot from the command
	// entry avoids a second parse; direct callers resolve once here, before any
	// resource is published.
	var database generatedAPIDatabase
	if opts.Kind == "api" {
		if opts.database != nil {
			database = *opts.database
		} else {
			resolved, err := resolveGeneratedAPIDatabase(opts.Directory, opts.ConfigPaths)
			if err != nil {
				return generateResult{}, err
			}
			database = resolved
		}
	}
	activePackage := packageName(opts.Name)
	if activePackage == "resource" && len(nameParts(opts.Name)) == 0 {
		return generateResult{}, fmt.Errorf("resource name %q is invalid", opts.Name)
	}
	// Every api generation writes migrations, so every api generation takes
	// the lock — managed or legacy. The lock is acquired before the manifest
	// is read so the whole read-decide-write sequence is exclusive.
	if opts.Kind == "api" {
		release, err := lockGeneration(opts.Directory)
		if err != nil {
			return generateResult{}, err
		}
		defer release()
	}
	managed, err := prepareManagedGeneration(opts.Directory, opts.Kind, activePackage)
	if err != nil {
		return generateResult{}, err
	}

	// The plan renders exactly what preview shows; generation only executes it.
	plan, err := planResource(ctx, opts, database, managed)
	if err != nil {
		return generateResult{}, err
	}
	for _, conflict := range plan.Conflicts {
		return generateResult{}, errors.New(conflict)
	}
	return executePlan(ctx, opts.Directory, plan, managed)
}

// executePlan publishes a conflict-free plan: resource files, migration pair,
// go.mod pins, and manifest registration with rollback on failure.
func executePlan(ctx context.Context, directory string, plan *resourcePlan, managed *managedGeneration) (generateResult, error) {
	activePackage := plan.data.PackageName
	internalDir := filepath.Join(directory, "internal")
	target := filepath.Join(internalDir, activePackage)
	if err := checkParentSymlinks(directory, target); err != nil {
		return generateResult{}, err
	}
	if err := os.MkdirAll(internalDir, 0755); err != nil {
		return generateResult{}, fmt.Errorf("create internal directory: %w", err)
	}
	temporary, err := os.MkdirTemp(internalDir, "."+activePackage+".tmp-")
	if err != nil {
		return generateResult{}, fmt.Errorf("create temporary resource package: %w", err)
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(temporary)
		}
	}()
	for _, file := range plan.Files {
		if err := ctx.Err(); err != nil {
			return generateResult{}, err
		}
		if err := os.WriteFile(filepath.Join(temporary, file.Name), file.Contents, 0644); err != nil {
			return generateResult{}, fmt.Errorf("write generated file %q: %w", file.Rel, err)
		}
	}
	if err := ctx.Err(); err != nil {
		return generateResult{}, err
	}
	if err := atomicdir.Publish(temporary, target); err != nil {
		return generateResult{}, fmt.Errorf("publish resource package: %w", err)
	}
	published = true
	var migrationFiles []string
	// Snapshot go.mod so a later rollback can undo dependency pins: applyPins
	// rewrites the file in place, and leaving pins behind after a failed
	// registration would strand requirements the resource no longer needs.
	goModPath := filepath.Join(directory, "go.mod")
	goModOriginal, goModReadErr := os.ReadFile(goModPath)
	goModExisted := goModReadErr == nil
	goModMode := os.FileMode(0644)
	if goModExisted {
		if info, statErr := os.Stat(goModPath); statErr == nil {
			goModMode = info.Mode().Perm()
		}
	}
	rollback := func() error {
		removeGeneratedFiles(directory, migrationFiles)
		var restoreErr error
		if goModExisted {
			restoreErr = os.WriteFile(goModPath, goModOriginal, goModMode)
		}
		return errors.Join(os.RemoveAll(target), restoreErr)
	}
	if plan.Kind == "api" && plan.Migration != nil {
		written, err := writeResourceMigration(directory, plan.migration)
		// Record partial writes before checking the error so rollback removes a
		// migration pair whose second file failed after the first was written.
		migrationFiles = written
		if err != nil {
			if removeErr := rollback(); removeErr != nil {
				return generateResult{}, fmt.Errorf("%w (rollback resource: %v)", err, removeErr)
			}
			return generateResult{}, err
		}
	}
	if err := applyPins(directory, plan.GoMod); err != nil {
		if removeErr := rollback(); removeErr != nil {
			return generateResult{}, fmt.Errorf("pin generated dependencies: %w (rollback resource: %v)", err, removeErr)
		}
		return generateResult{}, fmt.Errorf("pin generated dependencies: %w", err)
	}
	if managed != nil {
		if err := managed.register(plan.data, planFileSHAs(plan)); err != nil {
			if removeErr := rollback(); removeErr != nil {
				return generateResult{}, fmt.Errorf("register generated API: %w (rollback resource: %v)", err, removeErr)
			}
			return generateResult{}, fmt.Errorf("register generated API: %w", err)
		}
	}
	hint := strings.Join(plan.Warnings, "\n")
	return generateResult{Path: filepath.FromSlash(plan.Target), AdapterHint: hint}, nil
}

type managedGeneration struct {
	root             string
	manifest         scaffold.Manifest
	originalManifest []byte
	manifestMode     os.FileMode
}

func scaffoldManifestExists(root string) (bool, error) {
	path := filepath.Join(root, filepath.FromSlash(scaffold.ManifestPath))
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, fmt.Errorf("inspect scaffold manifest %q: %w", path, err)
}

// generateLockPath is the project-relative path of the exclusive generation
// lock.
const generateLockPath = ".bear/generate.lock"

// acquireGenerationLock takes the per-project generation lock and returns the
// held file together with its absolute path.
//
// Two concurrent `bear gen api` runs would otherwise interleave their writes to
// .bear/scaffold.json and internal/app/modules_gen.go, silently dropping one of
// the registrations.
//
// The lock records the owning pid and start time so that a lock left behind by a
// crashed run can be described instead of reported as a bare "file exists". A
// stale lock is deliberately not reclaimed automatically: deciding that no other
// process is generating in this project is the operator's call, which is also
// how git treats a leftover index.lock. Reclaiming it here would need pid
// liveness checks, and those differ per platform and misjudge a recycled pid.
func acquireGenerationLock(root string) (*os.File, string, error) {
	path := filepath.Join(root, filepath.FromSlash(generateLockPath))
	// A legacy project has no .bear directory yet; the lock is the first thing
	// to need it. Creating it is safe because release removes the lock and then
	// removes the directory again when nothing else occupies it.
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, path, fmt.Errorf("create generation lock directory %q: %w", filepath.Dir(path), err)
	}
	lock, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err == nil {
		if _, writeErr := fmt.Fprintf(lock, "pid=%d started=%s\n", os.Getpid(), time.Now().Format(time.RFC3339)); writeErr != nil {
			// A lock with no owner record still excludes other runs, so the
			// failure to describe the holder must not be reported as a lock.
			_ = lock.Close()
			_ = os.Remove(path)
			return nil, path, fmt.Errorf("record generation lock owner %q: %w", path, writeErr)
		}
		return lock, path, nil
	}
	if !errors.Is(err, os.ErrExist) {
		return nil, path, fmt.Errorf("acquire generation lock %q: %w", path, err)
	}
	return nil, path, fmt.Errorf(`generation lock %q is already held (%s).
Another "bear gen" may be running in this project. Wait for it to finish, or delete the lock and retry:
    rm %q`,
		path, describeGenerationLockHolder(path), path)
}

// lockGeneration takes the per-project generation lock for a command that
// writes generated API files and returns its release. Both managed and legacy
// (manifest-less) projects need it: both write migrations, and two concurrent
// runs would otherwise allocate the same migration version. Callers invoke it
// only for api generations; model/dto write neither a migration nor the
// registry.
func lockGeneration(root string) (func(), error) {
	lock, lockPath, err := acquireGenerationLock(root)
	if err != nil {
		return nil, err
	}
	return func() {
		if lock != nil {
			_ = lock.Close()
		}
		_ = os.Remove(lockPath)
		// Drop .bear again if this run created it for the lock and left it
		// empty; a managed project keeps its manifest, so Remove fails and is
		// ignored.
		_ = os.Remove(filepath.Dir(lockPath))
	}, nil
}

// describeGenerationLockHolder summarises the owner record of an existing lock.
// It never fails: an unreadable or empty lock still has to produce a usable
// recovery message.
func describeGenerationLockHolder(path string) string {
	contents, err := os.ReadFile(path)
	if err != nil {
		return "owner record unreadable"
	}
	fields := strings.Fields(string(contents))
	if len(fields) == 0 {
		return "owner record empty"
	}
	return strings.Join(fields, " ")
}

func prepareManagedGeneration(root, kind, packageName string) (*managedGeneration, error) {
	if kind != "api" {
		return nil, nil
	}
	exists, err := scaffoldManifestExists(root)
	if err != nil || !exists {
		return nil, err
	}

	managed := &managedGeneration{root: root}
	managed.manifest, err = scaffold.ReadManifest(root)
	if err != nil {
		return nil, err
	}
	for _, api := range managed.manifest.APIs {
		if api.Package == packageName {
			return nil, fmt.Errorf("generated API package %q is already registered", packageName)
		}
	}
	manifestPath := filepath.Join(root, filepath.FromSlash(scaffold.ManifestPath))
	managed.originalManifest, err = os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("snapshot scaffold manifest %q: %w", manifestPath, err)
	}
	info, err := os.Stat(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("inspect scaffold manifest %q: %w", manifestPath, err)
	}
	managed.manifestMode = info.Mode().Perm()
	return managed, nil
}

func (managed *managedGeneration) register(data resourceData, files map[string]string) error {
	_, manifestContents, registryContents, err := renderManifestUpdate(managed.manifest, data, files)
	if err != nil {
		return err
	}
	manifestPath := filepath.Join(managed.root, filepath.FromSlash(scaffold.ManifestPath))
	if err := writeGeneratedFileAtomic(manifestPath, manifestContents, managed.manifestMode); err != nil {
		return fmt.Errorf("write scaffold manifest: %w", err)
	}
	registryPath := filepath.Join(managed.root, filepath.FromSlash(scaffold.ModulesPath))
	if err := writeGeneratedFileAtomic(registryPath, registryContents, 0644); err != nil {
		restoreErr := writeGeneratedFileAtomic(manifestPath, managed.originalManifest, managed.manifestMode)
		if restoreErr != nil {
			return fmt.Errorf("write module registry: %w (restore scaffold manifest: %v)", err, restoreErr)
		}
		return fmt.Errorf("write module registry: %w", err)
	}
	return nil
}

func renderModuleRegistry(manifest scaffold.Manifest) ([]byte, error) {
	var output strings.Builder
	output.WriteString("// Code generated by bear. DO NOT EDIT.\n\npackage app\n\nimport (\n")
	fmt.Fprintf(&output, "\t_bear %q\n", "github.com/duiniwukenaihe/gin-bear/pkg/bear")
	for _, api := range manifest.APIs {
		fmt.Fprintf(&output, "\t%s %q\n", api.Package, manifest.Module+"/internal/"+api.Package)
	}
	output.WriteString(")\n\nfunc generatedModules() []_bear.Module {\n\treturn []_bear.Module{\n")
	for _, api := range manifest.APIs {
		fmt.Fprintf(&output, "\t\t%s.NewModule(),\n", api.Package)
	}
	output.WriteString("\t}\n}\n")
	formatted, err := format.Source([]byte(output.String()))
	if err != nil {
		return nil, fmt.Errorf("format module registry: %w", err)
	}
	return formatted, nil
}

func writeGeneratedFileAtomic(path string, contents []byte, mode os.FileMode) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0755); err != nil {
		return fmt.Errorf("create directory %q: %w", directory, err)
	}
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return fmt.Errorf("create temporary file for %q: %w", path, err)
	}
	temporaryPath := temporary.Name()
	closed := false
	defer func() {
		if !closed {
			_ = temporary.Close()
		}
		_ = os.Remove(temporaryPath)
	}()
	if err := temporary.Chmod(mode); err != nil {
		return fmt.Errorf("set temporary file mode for %q: %w", path, err)
	}
	if _, err := temporary.Write(contents); err != nil {
		return fmt.Errorf("write temporary file for %q: %w", path, err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync temporary file for %q: %w", path, err)
	}
	if err := temporary.Close(); err != nil {
		closed = true
		return fmt.Errorf("close temporary file for %q: %w", path, err)
	}
	closed = true
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace %q: %w", path, err)
	}
	return nil
}

func hasRequirement(file *modfile.File, path string) bool {
	for _, requirement := range file.Require {
		if requirement.Mod.Path == path {
			return true
		}
	}
	return false
}

func ensureDirectRequirement(file *modfile.File, path, version string) (bool, error) {
	for _, existing := range file.Require {
		if existing.Mod.Path != path {
			continue
		}
		targetVersion := version
		if semver.Compare(existing.Mod.Version, version) >= 0 {
			targetVersion = existing.Mod.Version
		}
		if existing.Mod.Version == targetVersion && !existing.Indirect {
			return false, nil
		}
		if err := file.DropRequire(path); err != nil {
			return false, fmt.Errorf("replace requirement %s: %w", path, err)
		}
		if err := file.AddRequire(path, targetVersion); err != nil {
			return false, fmt.Errorf("add direct requirement %s: %w", path, err)
		}
		return true, nil
	}
	if err := file.AddRequire(path, version); err != nil {
		return false, fmt.Errorf("add direct requirement %s: %w", path, err)
	}
	return true, nil
}

func templatesForKind(kind string) (map[string]string, error) {
	switch kind {
	case "api":
		return map[string]string{
			"controller.go":   controllerTemplate,
			"dto.go":          dtoTemplate,
			"model.go":        modelTemplate,
			"module.go":       moduleTemplate,
			"repository.go":   repositoryTemplate,
			"router.go":       routerTemplate,
			"service.go":      serviceTemplate,
			"service_test.go": serviceTestTemplate,
		}, nil
	case "model":
		return map[string]string{"model.go": modelTemplate}, nil
	case "dto":
		return map[string]string{"dto.go": dtoTemplate}, nil
	default:
		return nil, fmt.Errorf("unsupported generation type %q", kind)
	}
}

func executeResourceTemplate(name, source string, data resourceData) ([]byte, error) {
	tmpl, err := template.New(name).Option("missingkey=error").Parse(source)
	if err != nil {
		return nil, fmt.Errorf("parse resource template %q: %w", name, err)
	}
	var output strings.Builder
	if err := tmpl.Execute(&output, data); err != nil {
		return nil, fmt.Errorf("render resource template %q: %w", name, err)
	}
	return []byte(output.String()), nil
}

func parseResourceFields(raw string) ([]field, error) {
	if strings.TrimSpace(raw) == "" {
		return []field{defaultNameField()}, nil
	}
	parts := strings.Split(raw, ",")
	fields := make([]field, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		pair := strings.SplitN(strings.TrimSpace(part), ":", 2)
		if len(pair) != 2 || strings.TrimSpace(pair[0]) == "" || strings.TrimSpace(pair[1]) == "" {
			return nil, fmt.Errorf("invalid field definition %q (expected name:type)", part)
		}
		jsonName := jsonName(pair[0])
		name := titleName(pair[0])
		if _, exists := seen[name]; exists {
			return nil, fmt.Errorf("duplicate field %q", pair[0])
		}
		seen[name] = struct{}{}
		goType, validate, gormTag, err := fieldType(strings.ToLower(strings.TrimSpace(pair[1])))
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", pair[0], err)
		}
		fields = append(fields, field{
			Name:           name,
			GoType:         goType,
			JSONName:       jsonName,
			Validate:       validate,
			UpdateValidate: updateValidation(validate),
			GormTag:        gormTag,
		})
	}
	if _, hasName := seen["Name"]; !hasName {
		fields = append([]field{defaultNameField()}, fields...)
	}
	return fields, nil
}

func defaultNameField() field {
	return field{
		Name:           "Name",
		GoType:         "string",
		JSONName:       "name",
		Validate:       "required",
		UpdateValidate: "omitempty",
		GormTag:        "type:varchar(100)",
	}
}

func updateValidation(validate string) string {
	rules := []string{"omitempty"}
	for _, rule := range strings.Split(validate, ",") {
		rule = strings.TrimSpace(rule)
		if rule == "" || rule == "required" || rule == "omitempty" {
			continue
		}
		rules = append(rules, rule)
	}
	return strings.Join(rules, ",")
}

func fieldType(kind string) (string, string, string, error) {
	switch kind {
	case "string":
		return "string", "required", "type:varchar(255)", nil
	case "email":
		return "string", "required,email", "type:varchar(255)", nil
	case "url":
		return "string", "required,url", "type:varchar(255)", nil
	case "phone":
		return "string", "required", "type:varchar(50)", nil
	case "int", "int64", "int32":
		return "int64", "required,numeric", "type:bigint", nil
	case "int8", "int16":
		return "int", "required,numeric", "type:bigint", nil
	case "float", "float64":
		return "float64", "required,numeric", "type:decimal(10,2)", nil
	case "float32":
		return "float32", "required,numeric", "type:float", nil
	case "bool":
		return "bool", "", "type:tinyint(1)", nil
	case "time", "datetime":
		return "time.Time", "", "type:datetime", nil
	case "text", "longtext":
		return "string", "", "type:text", nil
	case "decimal":
		return "decimal.Decimal", "required", "type:decimal(10,2)", nil
	default:
		return "", "", "", fmt.Errorf("unsupported field type %q", kind)
	}
}

func resourceImports(fields []field) string {
	var imports []string
	for _, item := range fields {
		switch item.GoType {
		case "time.Time":
			imports = appendUnique(imports, "time")
		case "decimal.Decimal":
			imports = appendUnique(imports, "github.com/shopspring/decimal")
		}
	}
	if len(imports) == 0 {
		return ""
	}
	sort.Strings(imports)
	var output strings.Builder
	output.WriteString("import (\n")
	for _, path := range imports {
		fmt.Fprintf(&output, "\t%q\n", path)
	}
	output.WriteString(")")
	return output.String()
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func nameParts(value string) []string {
	raw := strings.FieldsFunc(strings.ToLower(value), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	parts := make([]string, 0, len(raw))
	for _, part := range raw {
		if part != "" {
			parts = append(parts, part)
		}
	}
	return parts
}

func titleName(value string) string {
	var output strings.Builder
	for _, part := range nameParts(value) {
		runes := []rune(part)
		runes[0] = unicode.ToUpper(runes[0])
		output.WriteString(string(runes))
	}
	if output.Len() == 0 {
		return "Resource"
	}
	result := output.String()
	if !unicode.IsLetter([]rune(result)[0]) {
		return "Resource" + result
	}
	return result
}

func packageName(value string) string {
	var output strings.Builder
	for _, part := range nameParts(value) {
		for _, r := range part {
			if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
				output.WriteRune(r)
			}
		}
	}
	if output.Len() == 0 {
		return "resource"
	}
	result := output.String()
	if result[0] < 'a' || result[0] > 'z' {
		return "resource" + result
	}
	return result
}

func routeName(value string) string {
	parts := nameParts(value)
	if len(parts) == 0 {
		return "resource"
	}
	return strings.Join(parts, "-")
}

func jsonName(value string) string {
	parts := nameParts(value)
	if len(parts) == 0 {
		return "field"
	}
	return strings.Join(parts, "_")
}

const modelTemplate = `package {{.PackageName}}
{{.Imports}}

type {{.Title}}Model struct {
	ID uint ` + "`gorm:\"primaryKey\" json:\"id\"`" + `
	{{- range .Fields}}
	{{.Name}} {{.GoType}} ` + "`gorm:\"{{.GormTag}}\" json:\"{{.JSONName}}\"`" + `
	{{- end}}
}

func (m *{{.Title}}Model) TableName() string { return "{{.RouteName}}" }
`

const dtoTemplate = `package {{.PackageName}}
{{.Imports}}

type {{.Title}}CreateDTO struct {
	{{- range .Fields}}
	{{.Name}} {{.GoType}} ` + "`json:\"{{.JSONName}}\" binding:\"{{.Validate}}\"`" + `
	{{- end}}
}

type {{.Title}}UpdateDTO struct {
	{{- range .Fields}}
	{{.Name}} *{{.GoType}} ` + "`json:\"{{.JSONName}}\" binding:\"{{.UpdateValidate}}\"`" + `
	{{- end}}
}

type {{.Title}}QueryDTO struct {
	Page int ` + "`form:\"page\" json:\"page\"`" + `
	PageSize int ` + "`form:\"page_size\" json:\"page_size\"`" + `
	Keyword string ` + "`form:\"keyword\" json:\"keyword\"`" + `
}

func (q *{{.Title}}QueryDTO) Normalize() {
	if q.Page <= 0 { q.Page = 1 }
	if q.PageSize <= 0 { q.PageSize = 20 }
	if q.PageSize > 100 { q.PageSize = 100 }
}

type {{.Title}}Response struct {
	ID uint ` + "`json:\"id\"`" + `
	{{- range .Fields}}
	{{.Name}} {{.GoType}} ` + "`json:\"{{.JSONName}}\"`" + `
	{{- end}}
}

type {{.Title}}ListResponse struct {
	Total int64 ` + "`json:\"total\"`" + `
	List []*{{.Title}}Response ` + "`json:\"list\"`" + `
}
`

const repositoryTemplate = `package {{.PackageName}}

import (
	"context"
	"errors"

	"github.com/duiniwukenaihe/gin-bear/pkg/bear"
	"gorm.io/gorm"
)

type {{.Title}}Repository struct {
	*bear.Repository[{{.Title}}Model]
	Adapter *bear.GormAdapter ` + "`inject:\"-\"`" + `
}

func (r *{{.Title}}Repository) Name() string { return "{{.Title}}Repository" }

func (r *{{.Title}}Repository) Init(_ context.Context) error {
	if r.Adapter == nil { return errors.New("{{.Title}}Repository requires GormAdapter") }
	r.Repository = bear.NewRepository[{{.Title}}Model](r.Adapter)
	return nil
}

func (r *{{.Title}}Repository) FindByID(ctx context.Context, id int64) (*{{.Title}}Model, error) {
	return r.FindOne(ctx, map[string]interface{}{"id": id})
}

func (r *{{.Title}}Repository) FindByCondition(ctx context.Context, query *{{.Title}}QueryDTO) ([]*{{.Title}}Model, error) {
	if query == nil { query = &{{.Title}}QueryDTO{} }
	query.Normalize()
	db := r.queryScope(ctx, query).Offset((query.Page - 1) * query.PageSize).Limit(query.PageSize)
	var list []*{{.Title}}Model
	err := db.Find(&list).Error
	return list, err
}

func (r *{{.Title}}Repository) Count(ctx context.Context, query *{{.Title}}QueryDTO) (int64, error) {
	var count int64
	err := r.queryScope(ctx, query).Count(&count).Error
	return count, err
}

func (r *{{.Title}}Repository) queryScope(ctx context.Context, query *{{.Title}}QueryDTO) *gorm.DB {
	db := r.DB(ctx).Model(&{{.Title}}Model{})
	if query != nil && query.Keyword != "" {
		db = db.Where("name LIKE ?", "%"+query.Keyword+"%")
	}
	return db
}

func (r *{{.Title}}Repository) Create(ctx context.Context, dto *{{.Title}}CreateDTO) (*{{.Title}}Model, error) {
	model := &{{.Title}}Model{
		{{- range .Fields}}
		{{.Name}}: dto.{{.Name}},
		{{- end}}
	}
	err := r.Repository.Create(ctx, model)
	return model, err
}

func (r *{{.Title}}Repository) Update(ctx context.Context, id int64, dto *{{.Title}}UpdateDTO) error {
	updates := map[string]interface{}{}
	{{- range .Fields}}
	if dto.{{.Name}} != nil { updates["{{.JSONName}}"] = *dto.{{.Name}} }
	{{- end}}
	if len(updates) == 0 { return bear.ErrInvalidParams.WithMsg("at least one field is required") }
	result := r.DB(ctx).Model(&{{.Title}}Model{}).Where("id = ?", id).Updates(updates)
	if result.Error != nil { return result.Error }
	if result.RowsAffected == 0 { return gorm.ErrRecordNotFound }
	return nil
}

func (r *{{.Title}}Repository) Delete(ctx context.Context, id int64) error {
	result := r.DB(ctx).Delete(&{{.Title}}Model{}, id)
	if result.Error != nil { return result.Error }
	if result.RowsAffected == 0 { return gorm.ErrRecordNotFound }
	return nil
}
`

const serviceTemplate = `package {{.PackageName}}

import (
	"context"
	"errors"

	"github.com/duiniwukenaihe/gin-bear/pkg/bear"
	"gorm.io/gorm"
)

type {{.Title}}Service struct { Repo *{{.Title}}Repository ` + "`inject:\"\"`" + ` }

func (s *{{.Title}}Service) Name() string { return "{{.Title}}Service" }

func (s *{{.Title}}Service) GetByID(ctx context.Context, id int64) (*{{.Title}}Response, error) {
	model, err := s.Repo.FindByID(ctx, id)
	if err != nil { return nil, map{{.Title}}ServiceError(err) }
	return s.toResponse(model), nil
}

func (s *{{.Title}}Service) Query(ctx context.Context, query *{{.Title}}QueryDTO) (*{{.Title}}ListResponse, error) {
	list, err := s.Repo.FindByCondition(ctx, query)
	if err != nil { return nil, err }
	total, err := s.Repo.Count(ctx, query)
	if err != nil { return nil, err }
	items := make([]*{{.Title}}Response, len(list))
	for i, model := range list { items[i] = s.toResponse(model) }
	return &{{.Title}}ListResponse{Total: total, List: items}, nil
}

func (s *{{.Title}}Service) Create(ctx context.Context, dto *{{.Title}}CreateDTO) (*{{.Title}}Response, error) {
	model, err := s.Repo.Create(ctx, dto)
	if err != nil { return nil, err }
	return s.toResponse(model), nil
}

func (s *{{.Title}}Service) Update(ctx context.Context, id int64, dto *{{.Title}}UpdateDTO) error {
	return map{{.Title}}ServiceError(s.Repo.Update(ctx, id, dto))
}

func (s *{{.Title}}Service) Delete(ctx context.Context, id int64) error {
	return map{{.Title}}ServiceError(s.Repo.Delete(ctx, id))
}

func map{{.Title}}ServiceError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) { return bear.ErrNotFound.WithErr(err) }
	return err
}

func (s *{{.Title}}Service) toResponse(model *{{.Title}}Model) *{{.Title}}Response {
	return &{{.Title}}Response{
		ID: model.ID,
		{{- range .Fields}}
		{{.Name}}: model.{{.Name}},
		{{- end}}
	}
}
`

const controllerTemplate = `package {{.PackageName}}

import (
	"net/http"
	"strconv"

	"github.com/duiniwukenaihe/gin-bear/pkg/bear"
	"github.com/gin-gonic/gin"
)

type {{.Title}}Controller struct { Service *{{.Title}}Service ` + "`inject:\"\"`" + ` }

func (c *{{.Title}}Controller) Name() string { return "{{.Title}}Controller" }

func (c *{{.Title}}Controller) Build(b *bear.Bear) {
	if err := c.BuildE(b); err != nil { panic(err) }
}

func (c *{{.Title}}Controller) BuildE(b *bear.Bear) error {
	if err := b.HandleE("GET", "/{{.RouteName}}", c.List); err != nil { return err }
	if err := b.HandleE("GET", "/{{.RouteName}}/:id", c.Get); err != nil { return err }
	if err := b.HandleE("POST", "/{{.RouteName}}", c.createWithStatus); err != nil { return err }
	if err := b.HandleE("PATCH", "/{{.RouteName}}/:id", c.Update); err != nil { return err }
	if err := b.HandleE("PUT", "/{{.RouteName}}/:id", c.Update); err != nil { return err }
	if err := b.HandleE("DELETE", "/{{.RouteName}}/:id", c.deleteWithStatus); err != nil { return err }
	return nil
}

func (c *{{.Title}}Controller) List(ctx *gin.Context, query *{{.Title}}QueryDTO) (*{{.Title}}ListResponse, error) {
	return c.Service.Query(ctx, query)
}

func (c *{{.Title}}Controller) Get(ctx *gin.Context) (*{{.Title}}Response, error) {
	id, err := parseID(ctx.Param("id"))
	if err != nil { return nil, err }
	return c.Service.GetByID(ctx, id)
}

func (c *{{.Title}}Controller) Create(ctx *gin.Context, request *{{.Title}}CreateDTO) (*{{.Title}}Response, error) {
	return c.Service.Create(ctx, request)
}

func (c *{{.Title}}Controller) createWithStatus(ctx *gin.Context, request *{{.Title}}CreateDTO) (bear.StatusResponse, error) {
	response, err := c.Create(ctx, request)
	if err != nil { return bear.StatusResponse{}, err }
	return bear.WithStatus(http.StatusCreated, response), nil
}

func (c *{{.Title}}Controller) Update(ctx *gin.Context, request *{{.Title}}UpdateDTO) (bear.Response, error) {
	id, err := parseID(ctx.Param("id"))
	if err != nil { return bear.Response{}, err }
	return bear.Success(nil), c.Service.Update(ctx, id, request)
}

func (c *{{.Title}}Controller) Delete(ctx *gin.Context) (bear.Response, error) {
	id, err := parseID(ctx.Param("id"))
	if err != nil { return bear.Response{}, err }
	return bear.Success(nil), c.Service.Delete(ctx, id)
}

func (c *{{.Title}}Controller) deleteWithStatus(ctx *gin.Context) (bear.StatusResponse, error) {
	_, err := c.Delete(ctx)
	if err != nil { return bear.StatusResponse{}, err }
	return bear.WithStatus(http.StatusNoContent, nil), nil
}

func parseID(raw string) (int64, error) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil { return 0, bear.ErrInvalidParams.WithErr(err) }
	if id <= 0 { return 0, bear.ErrInvalidParams.WithMsg("id must be positive") }
	return id, nil
}
`

const moduleTemplate = `package {{.PackageName}}

import "github.com/duiniwukenaihe/gin-bear/pkg/bear"

type Module struct {
	controller *{{.Title}}Controller
	service *{{.Title}}Service
	repository *{{.Title}}Repository
}

type {{.Title}}Module = Module

func NewModule() *Module {
	repository := &{{.Title}}Repository{}
	service := &{{.Title}}Service{Repo: repository}
	controller := &{{.Title}}Controller{Service: service}
	return &Module{controller: controller, service: service, repository: repository}
}

func (m *Module) Name() string { return "{{.Title}}Module" }

func (m *Module) Beans() []bear.Bean {
	if m.repository == nil { m.repository = &{{.Title}}Repository{} }
	if m.service == nil { m.service = &{{.Title}}Service{Repo: m.repository} }
	if m.controller == nil { m.controller = &{{.Title}}Controller{Service: m.service} }
	return []bear.Bean{m.repository, m.service, m.controller}
}

func (m *Module) Build(b *bear.Bear) {
	b.Mount("/api/v1", m.controller)
}

func (m *Module) BuildE(b *bear.Bear) error {
	return b.MountE("/api/v1", m.controller)
}
`

const routerTemplate = `package {{.PackageName}}

// Register additional resource routes from {{.Title}}Module.Build.
`

const serviceTestTemplate = `package {{.PackageName}}

import (
	"context"
	"errors"
	"testing"

	"github.com/duiniwukenaihe/gin-bear/pkg/bear"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func Test{{.Title}}ServiceToResponse(t *testing.T) {
	model := &{{.Title}}Model{}
	service := &{{.Title}}Service{}
	if response := service.toResponse(model); response == nil {
		t.Fatal("expected response")
	}
}

func Test{{.Title}}QueryDTONormalizeBounds(t *testing.T) {
	query := &{{.Title}}QueryDTO{}
	query.Normalize()
	if query.Page != 1 || query.PageSize != 20 {
		t.Fatalf("zero query normalized to page=%d size=%d, want 1/20", query.Page, query.PageSize)
	}
	query = &{{.Title}}QueryDTO{Page: -3, PageSize: 10000}
	query.Normalize()
	if query.Page != 1 || query.PageSize != 100 {
		t.Fatalf("overflowing query normalized to page=%d size=%d, want 1/100", query.Page, query.PageSize)
	}
}

func open{{.Title}}TestService(t *testing.T) *{{.Title}}Service {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("sql.DB failed: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&{{.Title}}Model{}); err != nil {
		t.Fatalf("AutoMigrate failed: %v", err)
	}
	repository := &{{.Title}}Repository{}
	repository.Adapter = &bear.GormAdapter{DB: db}
	if err := repository.Init(context.Background()); err != nil {
		t.Fatalf("repository Init failed: %v", err)
	}
	return &{{.Title}}Service{Repo: repository}
}

func Test{{.Title}}ServiceCRUDRoundTrip(t *testing.T) {
	service := open{{.Title}}TestService(t)
	ctx := context.Background()
	created, err := service.Create(ctx, &{{.Title}}CreateDTO{Name: "first"})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	fetched, err := service.GetByID(ctx, int64(created.ID))
	if err != nil {
		t.Fatalf("GetByID failed: %v", err)
	}
	if fetched.Name != "first" {
		t.Fatalf("GetByID name = %q, want first", fetched.Name)
	}
	listed, err := service.Query(ctx, &{{.Title}}QueryDTO{Page: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}
	if listed.Total != 1 || len(listed.List) != 1 {
		t.Fatalf("Query total/list = %d/%d, want 1/1", listed.Total, len(listed.List))
	}
	renamed := "second"
	if err := service.Update(ctx, int64(created.ID), &{{.Title}}UpdateDTO{Name: &renamed}); err != nil {
		t.Fatalf("Update failed: %v", err)
	}
	if err := service.Delete(ctx, int64(created.ID)); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	if _, err := service.GetByID(ctx, int64(created.ID)); !errors.Is(err, bear.ErrNotFound) {
		t.Fatalf("GetByID after delete = %v, want not found", err)
	}
	if err := service.Delete(ctx, int64(created.ID)); !errors.Is(err, bear.ErrNotFound) {
		t.Fatalf("second Delete = %v, want not found", err)
	}
}

func Test{{.Title}}ServicePropagatesCancellation(t *testing.T) {
	service := open{{.Title}}TestService(t)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.Query(cancelled, &{{.Title}}QueryDTO{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Query with canceled context = %v, want context.Canceled", err)
	}
}
`

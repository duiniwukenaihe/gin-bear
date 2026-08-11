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
)

type resourceOptions struct {
	Kind      string
	Name      string
	Fields    string
	Directory string
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
	command := &cobra.Command{
		Use:   "gen <type> <name>",
		Short: "Generate code (api|model|dto)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			kind := strings.ToLower(args[0])
			if kind != "api" && kind != "model" && kind != "dto" {
				return fmt.Errorf("unsupported generation type %q (supported: api, model, dto)", args[0])
			}
			currentDirectory, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("resolve project directory: %w", err)
			}
			directory, err := nearestGoModRoot(currentDirectory)
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
			generated, err := generateResource(cmd.Context(), resourceOptions{
				Kind:      kind,
				Name:      args[1],
				Fields:    fields,
				Directory: directory,
			})
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Generated %s\n", generated)
			if kind == "api" && !managedProject {
				fmt.Fprintf(cmd.OutOrStdout(), "No %s found; register %s.NewModule() manually with application.AddModule or application.AddModuleE.\n", scaffold.ManifestPath, packageName(args[1]))
			}
			return nil
		},
	}
	command.Flags().StringVarP(&fields, "fields", "f", "", "fields as name:type pairs")
	return command
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

func generateResource(ctx context.Context, opts resourceOptions) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if opts.Directory == "" {
		return "", errors.New("project directory is required")
	}
	packageName := packageName(opts.Name)
	if packageName == "resource" && len(nameParts(opts.Name)) == 0 {
		return "", fmt.Errorf("resource name %q is invalid", opts.Name)
	}
	fields, err := parseResourceFields(opts.Fields)
	if err != nil {
		return "", err
	}
	data := resourceData{
		PackageName: packageName,
		Title:       titleName(opts.Name),
		RouteName:   routeName(opts.Name),
		Fields:      fields,
		Imports:     resourceImports(fields),
	}
	managed, err := prepareManagedGeneration(opts.Directory, opts.Kind, packageName)
	if err != nil {
		return "", err
	}
	if managed != nil {
		defer managed.release()
	}

	templates, err := templatesForKind(opts.Kind)
	if err != nil {
		return "", err
	}
	rendered := make(map[string][]byte, len(templates))
	names := make([]string, 0, len(templates))
	for filename, source := range templates {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		contents, err := executeResourceTemplate(filename, source, data)
		if err != nil {
			return "", err
		}
		formatted, err := format.Source(contents)
		if err != nil {
			return "", fmt.Errorf("format generated file %q: %w", filename, err)
		}
		rendered[filename] = formatted
		names = append(names, filename)
	}
	sort.Strings(names)

	internalDir := filepath.Join(opts.Directory, "internal")
	target := filepath.Join(internalDir, packageName)
	if _, err := os.Lstat(target); err == nil {
		return "", fmt.Errorf("resource package %q already exists", target)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect resource package %q: %w", target, err)
	}
	if err := os.MkdirAll(internalDir, 0755); err != nil {
		return "", fmt.Errorf("create internal directory: %w", err)
	}
	temporary, err := os.MkdirTemp(internalDir, "."+packageName+".tmp-")
	if err != nil {
		return "", fmt.Errorf("create temporary resource package: %w", err)
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(temporary)
		}
	}()
	for _, filename := range names {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(temporary, filename), rendered[filename], 0644); err != nil {
			return "", fmt.Errorf("write generated file %q: %w", filename, err)
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := atomicdir.Publish(temporary, target); err != nil {
		return "", fmt.Errorf("publish resource package: %w", err)
	}
	published = true
	if err := pinResourceDependencies(opts.Directory, opts.Kind, fields); err != nil {
		if removeErr := os.RemoveAll(target); removeErr != nil {
			return "", fmt.Errorf("pin generated dependencies: %w (rollback resource: %v)", err, removeErr)
		}
		return "", fmt.Errorf("pin generated dependencies: %w", err)
	}
	if managed != nil {
		if err := managed.register(data); err != nil {
			if removeErr := os.RemoveAll(target); removeErr != nil {
				return "", fmt.Errorf("register generated API: %w (rollback resource: %v)", err, removeErr)
			}
			return "", fmt.Errorf("register generated API: %w", err)
		}
	}
	return filepath.Join("internal", packageName), nil
}

type managedGeneration struct {
	root             string
	manifest         scaffold.Manifest
	originalManifest []byte
	manifestMode     os.FileMode
	lock             *os.File
	lockPath         string
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

func prepareManagedGeneration(root, kind, packageName string) (*managedGeneration, error) {
	if kind != "api" {
		return nil, nil
	}
	exists, err := scaffoldManifestExists(root)
	if err != nil || !exists {
		return nil, err
	}

	lockPath := filepath.Join(root, ".bear", "generate.lock")
	lock, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, fmt.Errorf("acquire generation lock %q: %w", lockPath, err)
	}
	managed := &managedGeneration{root: root, lock: lock, lockPath: lockPath}
	prepared := false
	defer func() {
		if !prepared {
			managed.release()
		}
	}()

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
	prepared = true
	return managed, nil
}

func (managed *managedGeneration) release() {
	if managed == nil {
		return
	}
	if managed.lock != nil {
		_ = managed.lock.Close()
	}
	_ = os.Remove(managed.lockPath)
}

func (managed *managedGeneration) register(data resourceData) error {
	managed.manifest.APIs = append(managed.manifest.APIs, scaffold.GeneratedAPI{
		Name:       data.Title,
		Package:    data.PackageName,
		Path:       filepath.ToSlash(filepath.Join("internal", data.PackageName)),
		ModuleType: data.PackageName + ".Module",
	})
	sort.SliceStable(managed.manifest.APIs, func(i, j int) bool {
		if managed.manifest.APIs[i].Package == managed.manifest.APIs[j].Package {
			return managed.manifest.APIs[i].Name < managed.manifest.APIs[j].Name
		}
		return managed.manifest.APIs[i].Package < managed.manifest.APIs[j].Package
	})

	manifestContents, err := scaffold.MarshalManifest(managed.manifest)
	if err != nil {
		return fmt.Errorf("render scaffold manifest: %w", err)
	}
	registryContents, err := renderModuleRegistry(managed.manifest)
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

func pinResourceDependencies(directory, kind string, fields []field) error {
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
	for _, requirement := range requirements {
		if requirement.preserveExisting && hasRequirement(file, requirement.path) {
			continue
		}
		requirementChanged, err := ensureDirectRequirement(file, requirement.path, requirement.version)
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

import "testing"

func Test{{.Title}}ServiceToResponse(t *testing.T) {
	model := &{{.Title}}Model{}
	service := &{{.Title}}Service{}
	if response := service.toResponse(model); response == nil {
		t.Fatal("expected response")
	}
}
`

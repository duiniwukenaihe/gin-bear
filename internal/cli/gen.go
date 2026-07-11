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

	"github.com/spf13/cobra"
)

type resourceOptions struct {
	Kind      string
	Name      string
	Fields    string
	Directory string
}

type field struct {
	Name     string
	GoType   string
	JSONName string
	Validate string
	GormTag  string
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
			directory, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("resolve project directory: %w", err)
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
			return nil
		},
	}
	command.Flags().StringVarP(&fields, "fields", "f", "", "fields as name:type pairs")
	return command
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
	if err := os.Rename(temporary, target); err != nil {
		return "", fmt.Errorf("publish resource package: %w", err)
	}
	published = true
	return filepath.Join("internal", packageName), nil
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
		return []field{{Name: "Name", GoType: "string", JSONName: "name", Validate: "required", GormTag: "type:varchar(100)"}}, nil
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
		fields = append(fields, field{Name: name, GoType: goType, JSONName: jsonName, Validate: validate, GormTag: gormTag})
	}
	return fields, nil
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
	{{.Name}} *{{.GoType}} ` + "`json:\"{{.JSONName}}\"`" + `
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

	"github.com/duiniwukenaihe/gin-bear/pkg/bear"
)

type {{.Title}}Repository struct { *bear.Repository[{{.Title}}Model] }

func (r *{{.Title}}Repository) Name() string { return "{{.Title}}Repository" }

func (r *{{.Title}}Repository) FindByID(ctx context.Context, id int64) (*{{.Title}}Model, error) {
	return r.FindOne(ctx, map[string]interface{}{"id": id})
}

func (r *{{.Title}}Repository) FindByCondition(ctx context.Context, query *{{.Title}}QueryDTO) ([]*{{.Title}}Model, error) {
	if query == nil { query = &{{.Title}}QueryDTO{} }
	query.Normalize()
	db := r.DB(ctx).Offset((query.Page - 1) * query.PageSize).Limit(query.PageSize)
	return r.FindList(ctx, db)
}

func (r *{{.Title}}Repository) Count(ctx context.Context, query *{{.Title}}QueryDTO) (int64, error) {
	var count int64
	err := r.DB(ctx).Model(&{{.Title}}Model{}).Count(&count).Error
	return count, err
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
	if len(updates) == 0 { return nil }
	return r.DB(ctx).Model(&{{.Title}}Model{}).Where("id = ?", id).Updates(updates).Error
}

func (r *{{.Title}}Repository) Delete(ctx context.Context, id int64) error {
	return r.DB(ctx).Delete(&{{.Title}}Model{}, id).Error
}
`

const serviceTemplate = `package {{.PackageName}}

import "context"

type {{.Title}}Service struct { Repo *{{.Title}}Repository ` + "`inject:\"\"`" + ` }

func (s *{{.Title}}Service) Name() string { return "{{.Title}}Service" }

func (s *{{.Title}}Service) GetByID(ctx context.Context, id int64) (*{{.Title}}Response, error) {
	model, err := s.Repo.FindByID(ctx, id)
	if err != nil { return nil, err }
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
	return s.Repo.Update(ctx, id, dto)
}

func (s *{{.Title}}Service) Delete(ctx context.Context, id int64) error { return s.Repo.Delete(ctx, id) }

func (s *{{.Title}}Service) toResponse(model *{{.Title}}Model) *{{.Title}}Response {
	return &{{.Title}}Response{
		{{- range .Fields}}
		{{.Name}}: model.{{.Name}},
		{{- end}}
	}
}
`

const controllerTemplate = `package {{.PackageName}}

import (
	"strconv"

	"github.com/duiniwukenaihe/gin-bear/pkg/bear"
	"github.com/gin-gonic/gin"
)

type {{.Title}}Controller struct { Service *{{.Title}}Service ` + "`inject:\"\"`" + ` }

func (c *{{.Title}}Controller) Name() string { return "{{.Title}}Controller" }

func (c *{{.Title}}Controller) Build(b *bear.Bear) {
	b.Handle("GET", "/{{.RouteName}}", c.List)
	b.Handle("GET", "/{{.RouteName}}/:id", c.Get)
	b.Handle("POST", "/{{.RouteName}}", c.Create)
	b.Handle("PUT", "/{{.RouteName}}/:id", c.Update)
	b.Handle("DELETE", "/{{.RouteName}}/:id", c.Delete)
}

func (c *{{.Title}}Controller) List(ctx *gin.Context, query *{{.Title}}QueryDTO) (interface{}, error) {
	return c.Service.Query(ctx, query)
}

func (c *{{.Title}}Controller) Get(ctx *gin.Context) (interface{}, error) {
	id, err := parseID(ctx.Param("id"))
	if err != nil { return nil, err }
	return c.Service.GetByID(ctx, id)
}

func (c *{{.Title}}Controller) Create(ctx *gin.Context, request *{{.Title}}CreateDTO) (interface{}, error) {
	return c.Service.Create(ctx, request)
}

func (c *{{.Title}}Controller) Update(ctx *gin.Context) (interface{}, error) {
	id, err := parseID(ctx.Param("id"))
	if err != nil { return nil, err }
	request := &{{.Title}}UpdateDTO{}
	if err := ctx.ShouldBindJSON(request); err != nil { return nil, bear.ErrInvalidParams.WithErr(err) }
	return bear.Success(nil), c.Service.Update(ctx, id, request)
}

func (c *{{.Title}}Controller) Delete(ctx *gin.Context) (interface{}, error) {
	id, err := parseID(ctx.Param("id"))
	if err != nil { return nil, err }
	return bear.Success(nil), c.Service.Delete(ctx, id)
}

func parseID(raw string) (int64, error) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil { return 0, bear.ErrInvalidParams.WithErr(err) }
	return id, nil
}
`

const moduleTemplate = `package {{.PackageName}}

import "github.com/duiniwukenaihe/gin-bear/pkg/bear"

type {{.Title}}Module struct{}

func (m *{{.Title}}Module) Name() string { return "{{.Title}}Module" }

func (m *{{.Title}}Module) Beans() []bear.Bean {
	return []bear.Bean{&{{.Title}}Service{}, &{{.Title}}Repository{}}
}

func (m *{{.Title}}Module) Build(b *bear.Bear) {
	b.Mount("/api/v1", &{{.Title}}Controller{})
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

package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
	"unicode"

	"github.com/spf13/cobra"
)

var (
	fields string // 额外字段，如: name:string,age:int,email:string
)

var genCmd = &cobra.Command{
	Use:   "gen [type] [name]",
	Short: "Generate code (api|model|dto)",
	Long: `Generate boilerplate code for Controller, Service, Repository, Model, DTO.

Types:
  api     - Generate full CRUD API (Controller, Service, Repository, Model, DTO, Module)
  model   - Generate Model only
  dto     - Generate DTO only

Examples:
  bear gen api user
  bear gen api product --fields "name:string,price:float,stock:int"
  bear gen model order
  bear gen dto user`,
	Args: cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		genType := args[0]
		name := args[1]

		switch genType {
		case "api":
			generateAPI(name, fields)
		case "model":
			generateModel(name, fields)
		case "dto":
			generateDTO(name, fields)
		default:
			fmt.Printf("Unsupported type: %s\n", genType)
			fmt.Println("Supported types: api, model, dto")
			os.Exit(1)
		}
	},
}

func init() {
	genCmd.Flags().StringVarP(&fields, "fields", "f", "", "Additional fields (e.g., name:string,age:int,email:string)")
	rootCmd.AddCommand(genCmd)
}

type TemplateData struct {
	PackageName string  // user
	Title       string  // User
	Name        string  // user
	ModuleName  string  // bear (or project name)
	Fields      []Field // 额外字段
	FieldsStr   string  // 字段字符串
}

type Field struct {
	Name     string // Name
	Type     string // Type
	GoType   string // GoType
	JsonTag  string // json tag
	Validate string // validation
	GormTag  string // gorm tag
}

func parseFields(fieldsStr string) []Field {
	if fieldsStr == "" {
		return []Field{
			{Name: "Name", Type: "string", GoType: "string", JsonTag: "name", Validate: "required", GormTag: "type:varchar(100)"},
		}
	}

	var fields []Field
	parts := strings.Split(fieldsStr, ",")
	for _, part := range parts {
		kv := strings.Split(strings.TrimSpace(part), ":")
		if len(kv) != 2 {
			continue
		}
		name := toTitle(kv[0])
		fieldType := kv[1]
		goType := getGoType(fieldType)
		validate := getValidate(fieldType)
		gormTag := getGormTag(fieldType)

		fields = append(fields, Field{
			Name:     name,
			Type:     fieldType,
			GoType:   goType,
			JsonTag:  strings.ToLower(kv[0]),
			Validate: validate,
			GormTag:  gormTag,
		})
	}
	return fields
}

func getGoType(t string) string {
	switch strings.ToLower(t) {
	case "string":
		return "string"
	case "int", "int64", "int32":
		return "int64"
	case "int8", "int16":
		return "int"
	case "float", "float64":
		return "float64"
	case "float32":
		return "float32"
	case "bool":
		return "bool"
	case "time", "datetime":
		return "time.Time"
	case "text", "longtext":
		return "string"
	case "decimal":
		return "float64"
	default:
		return "string"
	}
}

func getValidate(t string) string {
	switch strings.ToLower(t) {
	case "string":
		return "required"
	case "int", "int64", "int32", "int8", "int16":
		return "required,numeric"
	case "float", "float64", "float32":
		return "required,numeric"
	case "email":
		return "required,email"
	case "url":
		return "required,url"
	case "phone":
		return "required"
	case "datetime":
		return ""
	default:
		return ""
	}
}

func getGormTag(t string) string {
	switch strings.ToLower(t) {
	case "string":
		return "type:varchar(255)"
	case "int", "int64", "int32", "int8", "int16":
		return "type:bigint"
	case "float", "float64":
		return "type:decimal(10,2)"
	case "float32":
		return "type:float"
	case "bool":
		return "type:tinyint(1)"
	case "time", "datetime":
		return "type:datetime"
	case "text", "longtext":
		return "type:text"
	case "decimal":
		return "type:decimal(10,2)"
	default:
		return ""
	}
}

func generateAPI(name string, fieldsStr string) {
	name = strings.ToLower(name)
	title := toTitle(name)
	moduleName := getModuleName()
	fields := parseFields(fieldsStr)

	data := TemplateData{
		PackageName: name,
		Title:       title,
		Name:        name,
		ModuleName:  moduleName,
		Fields:      fields,
		FieldsStr:   fieldsStr,
	}

	// Create directory pkg/<name>
	dir := filepath.Join("pkg", name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		fmt.Printf("Failed to create directory: %v\n", err)
		os.Exit(1)
	}

	// Generate files
	files := map[string]string{
		"model.go":        modelTmpl,
		"dto.go":          dtoTmpl,
		"repository.go":   repositoryTmpl,
		"service.go":      serviceTmpl,
		"controller.go":   controllerTmpl,
		"module.go":       moduleTmpl,
		"router.go":       routerTmpl,
		"test_example.go": testTmpl,
	}

	for filename, tmplStr := range files {
		path := filepath.Join(dir, filename)
		if _, err := os.Stat(path); err == nil {
			fmt.Printf("Skipping %s (already exists)\n", path)
			continue
		}

		tmpl, err := template.New(filename).Parse(tmplStr)
		if err != nil {
			fmt.Printf("Template parse error: %v\n", err)
			continue
		}

		f, err := os.Create(path)
		if err != nil {
			fmt.Printf("Failed to create file %s: %v\n", path, err)
			continue
		}
		defer f.Close()

		if err := tmpl.Execute(f, data); err != nil {
			fmt.Printf("Template execute error: %v\n", err)
		}
		fmt.Printf("Generated %s\n", path)
	}

	printSuccess(name, title)
}

func generateModel(name string, fieldsStr string) {
	name = strings.ToLower(name)
	title := toTitle(name)
	moduleName := getModuleName()
	fields := parseFields(fieldsStr)

	dir := filepath.Join("pkg", name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		fmt.Printf("Failed to create directory: %v\n", err)
		os.Exit(1)
	}

	data := TemplateData{
		PackageName: name,
		Title:       title,
		Name:        name,
		ModuleName:  moduleName,
		Fields:      fields,
	}

	path := filepath.Join(dir, "model.go")
	if _, err := os.Stat(path); err == nil {
		fmt.Printf("Skipping %s (already exists)\n", path)
	} else {
		tmpl, _ := template.New("model.go").Parse(modelTmpl)
		f, _ := os.Create(path)
		tmpl.Execute(f, data)
		fmt.Printf("Generated %s\n", path)
	}
	fmt.Println("\n✅ Model generated!")
}

func generateDTO(name string, fieldsStr string) {
	name = strings.ToLower(name)
	title := toTitle(name)
	moduleName := getModuleName()
	fields := parseFields(fieldsStr)

	dir := filepath.Join("pkg", name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		fmt.Printf("Failed to create directory: %v\n", err)
		os.Exit(1)
	}

	data := TemplateData{
		PackageName: name,
		Title:       title,
		Name:        name,
		ModuleName:  moduleName,
		Fields:      fields,
	}

	path := filepath.Join(dir, "dto.go")
	if _, err := os.Stat(path); err == nil {
		fmt.Printf("Skipping %s (already exists)\n", path)
	} else {
		tmpl, _ := template.New("dto.go").Parse(dtoTmpl)
		f, _ := os.Create(path)
		tmpl.Execute(f, data)
		fmt.Printf("Generated %s\n", path)
	}
	fmt.Println("\n✅ DTO generated!")
}

func printSuccess(name, title string) {
	fmt.Println("\n" + strings.Repeat("=", 50))
	fmt.Println("✅ Generated successfully!")
	fmt.Println("\n📝 Next steps:")
	fmt.Printf("  1. Register module in main.go:\n")
	fmt.Printf("     app.AddModule(&%s.%sModule{})\n\n", name, title)
	fmt.Printf("  2. Run: go run main.go\n")
	fmt.Printf("\n📦 Available endpoints:")
	fmt.Printf("\n  GET    /api/v1/%s          - List %s (with pagination)\n", name, name)
	fmt.Printf("  GET    /api/v1/%s/:id      - Get %s by ID\n", name, name)
	fmt.Printf("  POST   /api/v1/%s          - Create %s\n", name, name)
	fmt.Printf("  PUT    /api/v1/%s/:id      - Update %s\n", name, name)
	fmt.Printf("  DELETE /api/v1/%s/:id      - Delete %s\n", name, name)
	fmt.Println(strings.Repeat("=", 50))
}

func toTitle(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

func getModuleName() string {
	content, err := os.ReadFile("go.mod")
	if err != nil {
		return "bear" // fallback
	}
	lines := strings.Split(string(content), "\n")
	if len(lines) > 0 && strings.HasPrefix(lines[0], "module ") {
		return strings.TrimSpace(strings.TrimPrefix(lines[0], "module "))
	}
	return "bear"
}

// ==================== Templates ====================

const modelTmpl = `package {{.PackageName}}

// {{.Title}}Model - {{.Title}} 数据模型
// @model {{.Title}}
type {{.Title}}Model struct {
	ID   uint   ` + "`gorm:\"primaryKey\"`" + `
	{{- range .Fields}}
	{{.Name}} {{.GoType}} ` + "`gorm:\"{{.GormTag}}\" json:\"{{.JsonTag}}\"`" + `
	{{- end}}
}

// TableName 表名
func (m *{{.Title}}Model) TableName() string {
	return "{{.Name}}"
}
`

const dtoTmpl = `package {{.PackageName}}

// ==================== DTOs ====================

// {{.Title}}CreateDTO - 创建 DTO
type {{.Title}}CreateDTO struct {
	{{- range .Fields}}
	{{.Name}} {{.GoType}} ` + "`json:\"{{.JsonTag}}\" binding:\"{{.Validate}}\"`" + `
	{{- end}}
}

// {{.Title}}UpdateDTO - 更新 DTO
type {{.Title}}UpdateDTO struct {
	{{- range .Fields}}
	{{.Name}} {{.GoType}} ` + "`json:\"{{.JsonTag}}\"`" + `
	{{- end}}
}

// {{.Title}}QueryDTO - 查询 DTO
type {{.Title}}QueryDTO struct {
	Page     int    ` + "`form:\"page\" json:\"page\"`" + `
	PageSize int    ` + "`form:\"page_size\" json:\"page_size\"`" + `
	Keyword  string ` + "`form:\"keyword\" json:\"keyword\"`" + `
}

// {{.Title}}Response - 响应 DTO
type {{.Title}}Response struct {
	{{- range .Fields}}
	{{.Name}} {{.GoType}} ` + "`json:\"{{.JsonTag}}\"`" + `
	{{- end}}
}

// {{.Title}}ListResponse - 列表响应
type {{.Title}}ListResponse struct {
	Total int64                 ` + "`json:\"total\"`" + `
	List  []*{{.Title}}Response ` + "`json:\"list\"`" + `
}
`

const repositoryTmpl = `package {{.PackageName}}

import (
	"context"

	"{{.ModuleName}}/pkg/bear"
)

// {{.Title}}Repository - {{.Title}} 仓库
type {{.Title}}Repository struct {
	*bear.Repository[{{.Title}}Model]
}

// Name 实现 Bean 接口
func (r *{{.Title}}Repository) Name() string {
	return "{{.Title}}Repository"
}

// FindByID 根据 ID 查询
func (r *{{.Title}}Repository) FindByID(ctx context.Context, id int64) (*{{.Title}}Model, error) {
	return r.FindOne(ctx, map[string]interface{}{"id": id})
}

// FindByCondition 条件查询
func (r *{{.Title}}Repository) FindByCondition(ctx context.Context, query *{{.Title}}QueryDTO) ([]*{{.Title}}Model, error) {
	db := r.DB(ctx)

	// 分页
	if query.Page > 0 {
		offset := (query.Page - 1) * query.PageSize
		db = db.Offset(offset).Limit(query.PageSize)
	} else {
		db = db.Limit(100)
	}

	// 关键词搜索
	if query.Keyword != "" {
		// db = db.Where("name LIKE ?", "%"+query.Keyword+"%")
	}

	return r.FindList(ctx, db)
}

// Count 统计数量
func (r *{{.Title}}Repository) Count(ctx context.Context, query *{{.Title}}QueryDTO) (int64, error) {
	db := r.DB(ctx)
	if query.Keyword != "" {
		// db = db.Where("name LIKE ?", "%"+query.Keyword+"%")
	}

	var count int64
	err := db.Model(&{{.Title}}Model{}).Count(&count).Error
	return count, err
}

// Create 创建
func (r *{{.Title}}Repository) Create(ctx context.Context, dto *{{.Title}}CreateDTO) (*{{.Title}}Model, error) {
	model := &{{.Title}}Model{
		{{- range .Fields}}
		{{.Name}}: {{.GetDefaultValue}},
		{{- end}}
	}
	err := r.Repository.Create(ctx, model)
	return model, err
}

// Update 更新
func (r *{{.Title}}Repository) Update(ctx context.Context, id int64, dto *{{.Title}}UpdateDTO) error {
	updates := map[string]interface{}{
		{{- range .Fields}}
		"{{.Name}}": dto.{{.Name}},
		{{- end}}
	}
	return r.DB(ctx).Model(&{{.Title}}Model{}).Where("id = ?", id).Updates(updates).Error
}

// Delete 删除
func (r *{{.Title}}Repository) Delete(ctx context.Context, id int64) error {
	return r.DB(ctx).Delete(&{{.Title}}Model{}, id).Error
}
`

const serviceTmpl = `package {{.PackageName}}

import (
	"context"
)

// {{.Title}}Service - {{.Title}} 服务
type {{.Title}}Service struct {
	Repo *{{.Title}}Repository ` + "`inject:\"\"`" + `
}

// Name 实现 Bean 接口
func (s *{{.Title}}Service) Name() string {
	return "{{.Title}}Service"
}

// GetByID 根据 ID 获取
func (s *{{.Title}}Service) GetByID(ctx context.Context, id int64) (*{{.Title}}Response, error) {
	model, err := s.Repo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return s.toResponse(model), nil
}

// Query 查询列表
func (s *{{.Title}}Service) Query(ctx context.Context, query *{{.Title}}QueryDTO) (*{{.Title}}ListResponse, error) {
	list, err := s.Repo.FindByCondition(ctx, query)
	if err != nil {
		return nil, err
	}

	total, err := s.Repo.Count(ctx, query)
	if err != nil {
		return nil, err
	}

	items := make([]*{{.Title}}Response, len(list))
	for i, m := range list {
		items[i] = s.toResponse(m)
	}

	return &{{.Title}}ListResponse{
		Total: total,
		List:  items,
	}, nil
}

// Create 创建
func (s *{{.Title}}Service) Create(ctx context.Context, dto *{{.Title}}CreateDTO) (*{{.Title}}Response, error) {
	model, err := s.Repo.Create(ctx, dto)
	if err != nil {
		return nil, err
	}
	return s.toResponse(model), nil
}

// Update 更新
func (s *{{.Title}}Service) Update(ctx context.Context, id int64, dto *{{.Title}}UpdateDTO) error {
	return s.Repo.Update(ctx, id, dto)
}

// Delete 删除
func (s *{{.Title}}Service) Delete(ctx context.Context, id int64) error {
	return s.Repo.Delete(ctx, id)
}

// toResponse 转换为响应
func (s *{{.Title}}Service) toResponse(model *{{.Title}}Model) *{{.Title}}Response {
	return &{{.Title}}Response{
		{{- range .Fields}}
		{{.Name}}: model.{{.Name}},
		{{- end}}
	}
}
`

const controllerTmpl = `package {{.PackageName}}

import (
	"strconv"

	"github.com/gin-gonic/gin"
	"{{.ModuleName}}/pkg/bear"
)

// {{.Title}}Controller - {{.Title}} 控制器
type {{.Title}}Controller struct {
	Service *{{.Title}}Service ` + "`inject:\"\"`" + `
}

// Name 实现 Bean 接口
func (c *{{.Title}}Controller) Name() string {
	return "{{.Title}}Controller"
}

// Build 注册路由
func (c *{{.Title}}Controller) Build(b *bear.Bear) {
	b.Handle("GET", "/{{.Name}}", c.List)
	b.Handle("GET", "/{{.Name}}/:id", c.Get)
	b.Handle("POST", "/{{.Name}}", c.Create)
	b.Handle("PUT", "/{{.Name}}/:id", c.Update)
	b.Handle("DELETE", "/{{.Name}}/:id", c.Delete)
}

// List 获取列表
// @Summary 获取{{.Title}}列表
// @Tags {{.Title}}
// @Accept json
// @Produce json
// @Param query query {{.Title}}QueryDTO false "查询参数"
// @Success 200 {object} {{.Title}}ListResponse
// @Router /api/v1/{{.Name}} [get]
func (c *{{.Title}}Controller) List(ctx *gin.Context, query *{{.Title}}QueryDTO) (interface{}, error) {
	if query.PageSize == 0 {
		query.PageSize = 20
	}
	return c.Service.Query(ctx, query)
}

// Get 获取单个
// @Summary 获取{{.Title}}
// @Tags {{.Title}}
// @Accept json
// @Produce json
// @Param id path int true "ID"
// @Success 200 {object} {{.Title}}Response
// @Router /api/v1/{{.Name}}/{id} [get]
func (c *{{.Title}}Controller) Get(ctx *gin.Context) (interface{}, error) {
	id, err := parseID(ctx.Param("id"))
	if err != nil {
		return nil, err
	}
	return c.Service.GetByID(ctx, id)
}

// Create 创建
// @Summary 创建{{.Title}}
// @Tags {{.Title}}
// @Accept json
// @Produce json
// @Param body body {{.Title}}CreateDTO true "请求体"
// @Success 200 {object} {{.Title}}Response
// @Router /api/v1/{{.Name}} [post]
func (c *{{.Title}}Controller) Create(ctx *gin.Context, req *{{.Title}}CreateDTO) (interface{}, error) {
	return c.Service.Create(ctx, req)
}

// Update 更新
// @Summary 更新{{.Title}}
// @Tags {{.Title}}
// @Accept json
// @Produce json
// @Param id path int true "ID"
// @Param body body {{.Title}}UpdateDTO true "请求体"
// @Success 200 {object} bear.Response
// @Router /api/v1/{{.Name}}/{id} [put]
func (c *{{.Title}}Controller) Update(ctx *gin.Context) (interface{}, error) {
	id, err := parseID(ctx.Param("id"))
	if err != nil {
		return nil, err
	}
	req := &{{.Title}}UpdateDTO{}
	if err := ctx.ShouldBindJSON(req); err != nil {
		return nil, bear.ErrInvalidParams.WithErr(err)
	}
	return bear.Success(nil), c.Service.Update(ctx, id, req)
}

// Delete 删除
// @Summary 删除{{.Title}}
// @Tags {{.Title}}
// @Accept json
// @Produce json
// @Param id path int true "ID"
// @Success 200 {object} bear.Response
// @Router /api/v1/{{.Name}}/{id} [delete]
func (c *{{.Title}}Controller) Delete(ctx *gin.Context) (interface{}, error) {
	id, err := parseID(ctx.Param("id"))
	if err != nil {
		return nil, err
	}
	return bear.Success(nil), c.Service.Delete(ctx, id)
}

// parseID 解析 ID
func parseID(s string) (int64, error) {
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, bear.ErrInvalidParams.WithErr(err)
	}
	return id, nil
}
`

const moduleTmpl = `package {{.PackageName}}

import (
	"{{.ModuleName}}/pkg/bear"
)

// {{.Title}}Module - {{.Title}} 模块
type {{.Title}}Module struct{}

// Name 模块名称
func (m *{{.Title}}Module) Name() string {
	return "{{.Title}}Module"
}

// Beans 注册 Bean
func (m *{{.Title}}Module) Beans() []bear.Bean {
	return []bear.Bean{
		&{{.Title}}Service{},
		&{{.Title}}Repository{},
	}
}

// Build 构建路由
func (m *{{.Title}}Module) Build(b *bear.Bear) {
	b.Mount("/api/v1", &{{.Title}}Controller{})
}
`

const routerTmpl = `package {{.PackageName}}

// 该文件可选 - 用于手动注册额外路由
// 如果需要在 Module Build 之外添加自定义路由，可在此文件操作

/*
示例:

import "github.com/gin-gonic/gin"

func RegisterCustomRoutes(b *bear.Bear) {
	b.Engine.GET("/custom/{{.Name}}", func(c *gin.Context) {
		c.JSON(200, gin.H{"message": "custom route"})
	})
}

然后在 module.go 中调用:
func (m *{{.Title}}Module) Build(b *bear.Bear) {
	b.Mount("/api/v1", &{{.Title}}Controller{})
	RegisterCustomRoutes(b)  // 添加自定义路由
}
*/
`

const testTmpl = `package {{.PackageName}}

// {{.Title}}Test 测试示例
// 完整测试需要初始化数据库连接
/*
import (
	"context"
	"testing"

	"{{.ModuleName}}/pkg/bear"
	"github.com/stretchr/testify/assert"
)

func Test{{.Title}}Service_Create(t *testing.T) {
	// 初始化
	app := bear.Ignite()
	_ = app

	// 测试逻辑...

	// 断言
	assert.True(t, true)
}
*/
`

// GetDefaultValue 获取字段默认值
func (f Field) GetDefaultValue() string {
	switch f.GoType {
	case "string":
		return `""`
	case "int64", "int", "int32", "int16", "int8":
		return "0"
	case "float64", "float32":
		return "0.0"
	case "bool":
		return "false"
	case "time.Time":
		return "time.Now()"
	default:
		return "nil"
	}
}

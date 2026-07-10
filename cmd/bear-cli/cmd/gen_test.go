package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestControllerTemplateParsesIDWithError(t *testing.T) {
	if strings.Contains(controllerTmpl, "mustParseID") {
		t.Fatal("controller template should not generate mustParseID")
	}
	if !strings.Contains(controllerTmpl, "parseID") {
		t.Fatal("controller template should generate parseID")
	}
	if !strings.Contains(controllerTmpl, "return nil, err") {
		t.Fatal("controller template should return parse errors")
	}
}

func TestGenerateAPIProducesCompilablePackage(t *testing.T) {
	dir := t.TempDir()
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repoRoot := filepath.Clean(filepath.Join(oldWd, "../../.."))
	t.Cleanup(func() {
		if err := os.Chdir(oldWd); err != nil {
			t.Fatal(err)
		}
	})
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	goMod := "module generated-smoke\n\ngo 1.25.0\n\nreplace github.com/duiniwukenaihe/gin-bear => " + repoRoot + "\n\nrequire github.com/duiniwukenaihe/gin-bear v0.0.0\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(goMod), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "pkg"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(repoRoot, "pkg", "bear"), filepath.Join(dir, "pkg", "bear")); err != nil {
		t.Fatal(err)
	}

	generateAPI("widget", "name:string")

	tidy := exec.Command("go", "mod", "tidy")
	tidy.Env = append(os.Environ(), "GOPROXY=https://goproxy.cn,direct")
	if out, err := tidy.CombinedOutput(); err != nil {
		t.Fatalf("generated module tidy failed: %v\n%s", err, string(out))
	}

	cmd := exec.Command("go", "test", "./...")
	cmd.Env = append(os.Environ(), "GOPROXY=https://goproxy.cn,direct")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("generated package should compile: %v\n%s", err, string(out))
	}
}

func TestParseFieldsSupportsProductionTypes(t *testing.T) {
	fields := parseFields("name:string,email:email,website:url,phone:phone,birthday:datetime,bio:text,price:decimal,enabled:bool")

	byJSON := make(map[string]Field, len(fields))
	for _, field := range fields {
		byJSON[field.JsonTag] = field
	}

	tests := map[string]struct {
		goType   string
		validate string
		gormTag  string
	}{
		"name":     {goType: "string", validate: "required", gormTag: "type:varchar(255)"},
		"email":    {goType: "string", validate: "required,email", gormTag: "type:varchar(255)"},
		"website":  {goType: "string", validate: "required,url", gormTag: "type:varchar(255)"},
		"phone":    {goType: "string", validate: "required", gormTag: "type:varchar(50)"},
		"birthday": {goType: "time.Time", validate: "", gormTag: "type:datetime"},
		"bio":      {goType: "string", validate: "", gormTag: "type:text"},
		"price":    {goType: "float64", validate: "required,numeric", gormTag: "type:decimal(10,2)"},
		"enabled":  {goType: "bool", validate: "", gormTag: "type:tinyint(1)"},
	}
	for name, want := range tests {
		got, ok := byJSON[name]
		if !ok {
			t.Fatalf("missing field %s in %#v", name, fields)
		}
		if got.GoType != want.goType || got.Validate != want.validate || got.GormTag != want.gormTag {
			t.Fatalf("%s = %#v, want go=%q validate=%q gorm=%q", name, got, want.goType, want.validate, want.gormTag)
		}
	}
}

func TestNameNormalizationProducesSafeIdentifiers(t *testing.T) {
	tests := []struct {
		raw         string
		wantTitle   string
		wantPackage string
		wantRoute   string
		wantJSON    string
	}{
		{
			raw:         "user-profile",
			wantTitle:   "UserProfile",
			wantPackage: "userprofile",
			wantRoute:   "user-profile",
			wantJSON:    "user_profile",
		},
		{
			raw:         "2026 user/profile!",
			wantTitle:   "Resource2026UserProfile",
			wantPackage: "resource2026userprofile",
			wantRoute:   "2026-user-profile",
			wantJSON:    "2026_user_profile",
		},
		{
			raw:         "!!!",
			wantTitle:   "Resource",
			wantPackage: "resource",
			wantRoute:   "resource",
			wantJSON:    "field",
		},
	}

	for _, tt := range tests {
		if got := toTitle(tt.raw); got != tt.wantTitle {
			t.Fatalf("toTitle(%q) = %q, want %q", tt.raw, got, tt.wantTitle)
		}
		if got := toPackageName(tt.raw); got != tt.wantPackage {
			t.Fatalf("toPackageName(%q) = %q, want %q", tt.raw, got, tt.wantPackage)
		}
		if got := toRouteName(tt.raw); got != tt.wantRoute {
			t.Fatalf("toRouteName(%q) = %q, want %q", tt.raw, got, tt.wantRoute)
		}
		if got := toJSONName(tt.raw); got != tt.wantJSON {
			t.Fatalf("toJSONName(%q) = %q, want %q", tt.raw, got, tt.wantJSON)
		}
	}
}

func TestGenerateAPIHandlesDashedNamesFieldsAndSafeUpdates(t *testing.T) {
	dir := t.TempDir()
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repoRoot := filepath.Clean(filepath.Join(oldWd, "../../.."))
	t.Cleanup(func() {
		if err := os.Chdir(oldWd); err != nil {
			t.Fatal(err)
		}
	})
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	goMod := "module generated-production\n\ngo 1.25.0\n\nreplace github.com/duiniwukenaihe/gin-bear => " + repoRoot + "\n\nrequire github.com/duiniwukenaihe/gin-bear v0.0.0\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(goMod), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "pkg"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(repoRoot, "pkg", "bear"), filepath.Join(dir, "pkg", "bear")); err != nil {
		t.Fatal(err)
	}

	generateAPI("user-profile", "name:string,email:email,age:int,birthday:datetime,bio:text")

	servicePath := filepath.Join(dir, "pkg", "userprofile", "repository.go")
	dtoPath := filepath.Join(dir, "pkg", "userprofile", "dto.go")
	modelPath := filepath.Join(dir, "pkg", "userprofile", "model.go")
	controllerPath := filepath.Join(dir, "pkg", "userprofile", "controller.go")
	for _, path := range []string{servicePath, dtoPath, modelPath, controllerPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected generated file %s: %v", path, err)
		}
	}

	repositoryText := readGeneratedFile(t, servicePath)
	for _, want := range []string{
		"Name: dto.Name",
		"Email: dto.Email",
		"Age: dto.Age",
		"Birthday: dto.Birthday",
		"Bio: dto.Bio",
		"if dto.Name != nil",
		`updates["name"] = *dto.Name`,
		"Normalize()",
	} {
		if !strings.Contains(repositoryText, want) {
			t.Fatalf("repository missing %q:\n%s", want, repositoryText)
		}
	}

	dtoText := readGeneratedFile(t, dtoPath)
	for _, want := range []string{
		`import "time"`,
		"Name *string",
		"Email *string",
		"Age *int64",
		"Birthday *time.Time",
		"func (q *UserProfileQueryDTO) Normalize()",
		"q.PageSize > 100",
	} {
		if !strings.Contains(dtoText, want) {
			t.Fatalf("dto missing %q:\n%s", want, dtoText)
		}
	}

	modelText := readGeneratedFile(t, modelPath)
	if !strings.Contains(modelText, `package userprofile`) || !strings.Contains(modelText, `import "time"`) {
		t.Fatalf("model should use safe package name and time import:\n%s", modelText)
	}

	tidy := exec.Command("go", "mod", "tidy")
	tidy.Env = append(os.Environ(), "GOPROXY=https://goproxy.cn,direct")
	if out, err := tidy.CombinedOutput(); err != nil {
		t.Fatalf("generated module tidy failed: %v\n%s", err, string(out))
	}

	cmd := exec.Command("go", "test", "./...")
	cmd.Env = append(os.Environ(), "GOPROXY=https://goproxy.cn,direct")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("generated package should compile: %v\n%s", err, string(out))
	}
}

func TestGenerateModelAndDTOCreatePackageFiles(t *testing.T) {
	dir := t.TempDir()
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(oldWd); err != nil {
			t.Fatal(err)
		}
	})
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("go.mod", []byte("module generated-model\n\ngo 1.25.0\n"), 0644); err != nil {
		t.Fatal(err)
	}

	generateModel("release-note", "published_at:datetime")
	generateDTO("release-note", "published_at:datetime")

	modelPath := filepath.Join(dir, "pkg", "releasenote", "model.go")
	dtoPath := filepath.Join(dir, "pkg", "releasenote", "dto.go")
	for _, path := range []string{modelPath, dtoPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected generated file %s: %v", path, err)
		}
	}

	if got := readGeneratedFile(t, modelPath); !strings.Contains(got, `package releasenote`) {
		t.Fatalf("unexpected model contents:\n%s", got)
	}
	if got := readGeneratedFile(t, dtoPath); !strings.Contains(got, `type ReleaseNoteQueryDTO struct`) {
		t.Fatalf("unexpected dto contents:\n%s", got)
	}
}

func TestGetModuleNameUsesGoModAndFallback(t *testing.T) {
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(oldWd); err != nil {
			t.Fatal(err)
		}
	})

	withGoMod := t.TempDir()
	if err := os.WriteFile(filepath.Join(withGoMod, "go.mod"), []byte("module example.com/app\n\ngo 1.25.0\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(withGoMod); err != nil {
		t.Fatal(err)
	}
	if got := getModuleName(); got != "example.com/app" {
		t.Fatalf("getModuleName() = %q", got)
	}

	withoutGoMod := t.TempDir()
	if err := os.Chdir(withoutGoMod); err != nil {
		t.Fatal(err)
	}
	if got := getModuleName(); got != "bear" {
		t.Fatalf("fallback getModuleName() = %q", got)
	}
}

func TestFieldGetDefaultValueCoversSupportedTypes(t *testing.T) {
	tests := map[string]string{
		"string":    `""`,
		"int64":     "0",
		"int":       "0",
		"float64":   "0.0",
		"bool":      "false",
		"time.Time": "time.Now()",
		"struct{}":  "nil",
	}

	for goType, want := range tests {
		if got := (Field{GoType: goType}).GetDefaultValue(); got != want {
			t.Fatalf("GetDefaultValue(%q) = %q, want %q", goType, got, want)
		}
	}
}

func TestFieldTypeMappingsCoverSupportedAliases(t *testing.T) {
	tests := []struct {
		fieldType string
		goType    string
		validate  string
		gormTag   string
	}{
		{fieldType: "STRING", goType: "string", validate: "required", gormTag: "type:varchar(255)"},
		{fieldType: "email", goType: "string", validate: "required,email", gormTag: "type:varchar(255)"},
		{fieldType: "url", goType: "string", validate: "required,url", gormTag: "type:varchar(255)"},
		{fieldType: "phone", goType: "string", validate: "required", gormTag: "type:varchar(50)"},
		{fieldType: "int32", goType: "int64", validate: "required,numeric", gormTag: "type:bigint"},
		{fieldType: "int8", goType: "int", validate: "required,numeric", gormTag: "type:bigint"},
		{fieldType: "float32", goType: "float32", validate: "required,numeric", gormTag: "type:float"},
		{fieldType: "bool", goType: "bool", validate: "", gormTag: "type:tinyint(1)"},
		{fieldType: "time", goType: "time.Time", validate: "", gormTag: "type:datetime"},
		{fieldType: "longtext", goType: "string", validate: "", gormTag: "type:text"},
		{fieldType: "decimal", goType: "float64", validate: "required,numeric", gormTag: "type:decimal(10,2)"},
		{fieldType: "unknown", goType: "string", validate: "", gormTag: ""},
	}

	for _, tt := range tests {
		t.Run(tt.fieldType, func(t *testing.T) {
			if got := getGoType(tt.fieldType); got != tt.goType {
				t.Fatalf("getGoType(%q) = %q, want %q", tt.fieldType, got, tt.goType)
			}
			if got := getValidate(tt.fieldType); got != tt.validate {
				t.Fatalf("getValidate(%q) = %q, want %q", tt.fieldType, got, tt.validate)
			}
			if got := getGormTag(tt.fieldType); got != tt.gormTag {
				t.Fatalf("getGormTag(%q) = %q, want %q", tt.fieldType, got, tt.gormTag)
			}
		})
	}
}

func readGeneratedFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

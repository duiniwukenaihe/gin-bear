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

func readGeneratedFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

package gen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScannerPreservesBuildTaggedSourceFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "feature.go")
	source := "//go:build feature\n\npackage fixture\n\ntype FeatureService struct {\n\tDependency any `inject:\"\"`\n}\n"
	if err := os.WriteFile(path, []byte(source), 0644); err != nil {
		t.Fatal(err)
	}

	infos, err := NewScanner(dir).Scan()
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("Scan() returned %d structs, want 1: %#v", len(infos), infos)
	}
	if infos[0].StructName != "FeatureService" {
		t.Fatalf("Scan() struct = %q, want FeatureService", infos[0].StructName)
	}
	if len(infos[0].Fields) != 1 || infos[0].Fields[0].FieldName != "Dependency" {
		t.Fatalf("Scan() fields = %#v", infos[0].Fields)
	}
}

// TestScannerRendersFieldTypesAsGoSource covers the type name the generated
// injector pastes into bear.Resolve[T]. Rendering the ast.Expr with %s produces
// fmt's debug form for a composite type, such as
// "&{%!s(token.Pos=57) Repository}", which is not valid Go.
func TestScannerRendersFieldTypesAsGoSource(t *testing.T) {
	dir := t.TempDir()
	source := "package fixture\n\nimport \"time\"\n\ntype Dependency struct{}\n\ntype Service struct {\n" +
		"\tRepo    *Dependency    `inject:\"\"`\n" +
		"\tStore   map[string]int `inject:\"\"`\n" +
		"\tItems   []Dependency   `inject:\"\"`\n" +
		"\tClock   time.Time      `inject:\"\"`\n" +
		"\tCounter int            `inject:\"\"`\n" +
		"\tPlain   Dependency     `inject:\"\"`\n" +
		"\tOpaque  any            `inject:\"\"`\n" +
		"}\n"
	if err := os.WriteFile(filepath.Join(dir, "service.go"), []byte(source), 0644); err != nil {
		t.Fatal(err)
	}

	infos, err := NewScanner(dir).Scan()
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("Scan() returned %d structs, want 1: %#v", len(infos), infos)
	}
	rendered := make(map[string]string, len(infos[0].Fields))
	for _, field := range infos[0].Fields {
		rendered[field.FieldName] = field.TypeName
	}
	for field, want := range map[string]string{
		"Repo":    "*Dependency",
		"Store":   "map[string]int",
		"Items":   "[]Dependency",
		"Clock":   "time.Time",
		"Counter": "int",
		"Plain":   "Dependency",
		"Opaque":  "any",
	} {
		if rendered[field] != want {
			t.Errorf("Scan() rendered %s as %q, want %q", field, rendered[field], want)
		}
	}
}

func TestGeneratorUsesRuntimeScopedStaticInjector(t *testing.T) {
	text := iocTemplate
	for _, want := range []string{
		`bear.RegisterRuntimeStaticInjector("{{.StructName}}", func(factory *bear.BeanFactory, obj interface{})`,
		`target.{{.FieldName}} = bear.Resolve[{{.TypeName}}](factory)`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("generated injector missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "bear.GetByType") {
		t.Fatalf("generated injector depends on global facade:\n%s", text)
	}
}

// TestScannerMatchesTheInjectTagKeyOnly pins that a tag which merely contains
// the substring "inject" is not read as an inject directive.
func TestScannerMatchesTheInjectTagKeyOnly(t *testing.T) {
	dir := t.TempDir()
	source := "package fixture\n\ntype Dependency struct{}\n\ntype Service struct {\n" +
		"\tCount  int    `json:\"inject_total\"`\n" +
		"\tColumn string `gorm:\"column:inject_id\"`\n" +
		"\tWanted *Dependency `inject:\"-\"`\n" +
		"}\n"
	if err := os.WriteFile(filepath.Join(dir, "service.go"), []byte(source), 0644); err != nil {
		t.Fatal(err)
	}

	infos, err := NewScanner(dir).Scan()
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("Scan() returned %d structs, want 1: %#v", len(infos), infos)
	}
	if len(infos[0].Fields) != 1 || infos[0].Fields[0].FieldName != "Wanted" {
		t.Fatalf("Scan() fields = %#v, want only the field tagged inject", infos[0].Fields)
	}
}

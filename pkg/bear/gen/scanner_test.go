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

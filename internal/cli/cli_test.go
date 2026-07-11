package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExecuteReportsHelpAndCommandErrors(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := Execute([]string{"--help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("help exit code = %d, stderr=%s", code, stderr.String())
	}
	for _, want := range []string{"Bear CLI", "new", "gen"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("help missing %q:\n%s", want, stdout.String())
		}
	}

	stdout.Reset()
	stderr.Reset()
	if code := Execute([]string{"gen", "unknown", "invoice"}, &stdout, &stderr); code != 1 {
		t.Fatalf("invalid generation exit code = %d", code)
	}
	if !strings.Contains(stderr.String(), "unsupported generation type") {
		t.Fatalf("invalid generation error is not actionable: %s", stderr.String())
	}
}

func TestExecuteNewCreatesProjectAndPreservesExistingDestination(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "billing")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Execute([]string{
		"new", "billing",
		"--module", "example.com/billing",
		"--directory", destination,
		"--framework-version", "v1.2.3",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("new exit code = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Created billing in "+destination) {
		t.Fatalf("new output = %q", stdout.String())
	}
	goMod, err := os.ReadFile(filepath.Join(destination, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(goMod), "module example.com/billing") {
		t.Fatalf("generated go.mod =\n%s", goMod)
	}

	marker := filepath.Join(destination, "keep.txt")
	if err := os.WriteFile(marker, []byte("keep"), 0644); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := Execute([]string{"new", "billing", "--directory", destination}, &stdout, &stderr); code != 1 {
		t.Fatalf("duplicate new exit code = %d", code)
	}
	if got, err := os.ReadFile(marker); err != nil || string(got) != "keep" {
		t.Fatalf("duplicate generation changed destination: content=%q err=%v", got, err)
	}
}

func TestExecuteGenPublishesResourceInCurrentProject(t *testing.T) {
	project := t.TempDir()
	t.Chdir(project)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Execute([]string{"gen", "dto", "audit-log", "--fields", "created_at:time,amount:decimal"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("gen exit code = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Generated internal/auditlog") {
		t.Fatalf("gen output = %q", stdout.String())
	}
	contents, err := os.ReadFile(filepath.Join(project, "internal", "auditlog", "dto.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"time.Time", "decimal.Decimal", `json:"created_at"`} {
		if !strings.Contains(string(contents), want) {
			t.Fatalf("generated dto missing %q:\n%s", want, contents)
		}
	}
}

func TestGenerateDecimalResourcePinsDependencyVersion(t *testing.T) {
	project := t.TempDir()
	goModPath := filepath.Join(project, "go.mod")
	if err := os.WriteFile(goModPath, []byte("module example.com/invoice\n\ngo 1.25.12\n"), 0644); err != nil {
		t.Fatal(err)
	}

	if _, err := generateResource(context.Background(), resourceOptions{
		Kind:      "api",
		Name:      "invoice",
		Fields:    "amount:decimal",
		Directory: project,
	}); err != nil {
		t.Fatal(err)
	}

	goMod, err := os.ReadFile(goModPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(goMod), "github.com/shopspring/decimal v1.4.0") {
		t.Fatalf("generated decimal dependency is not pinned:\n%s", goMod)
	}
}

func TestGenerateResourceCoversKindsAndRejectsUnsafeInputs(t *testing.T) {
	project := t.TempDir()
	fields := strings.Join([]string{
		"title:string", "email:email", "website:url", "phone:phone",
		"count:int", "small:int8", "ratio:float", "precise:float32",
		"enabled:bool", "created_at:datetime", "notes:text", "amount:decimal",
	}, ",")

	for _, kind := range []string{"api", "model", "dto"} {
		name := kind + "-record"
		generated, err := generateResource(context.Background(), resourceOptions{
			Kind: kind, Name: name, Fields: fields, Directory: project,
		})
		if err != nil {
			t.Fatalf("generate %s: %v", kind, err)
		}
		if generated != filepath.Join("internal", kind+"record") {
			t.Fatalf("generate %s path = %q", kind, generated)
		}
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := generateResource(cancelled, resourceOptions{Kind: "dto", Name: "cancelled", Directory: project}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled generation error = %v", err)
	}
	for _, tt := range []struct {
		name string
		opts resourceOptions
		want string
	}{
		{name: "directory", opts: resourceOptions{Kind: "dto", Name: "item"}, want: "project directory is required"},
		{name: "resource name", opts: resourceOptions{Kind: "dto", Name: "!!!", Directory: project}, want: "resource name"},
		{name: "field", opts: resourceOptions{Kind: "dto", Name: "bad-field", Fields: "missing-type", Directory: project}, want: "invalid field definition"},
		{name: "kind", opts: resourceOptions{Kind: "service", Name: "bad-kind", Directory: project}, want: "unsupported generation type"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := generateResource(context.Background(), tt.opts)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}

	if _, err := generateResource(context.Background(), resourceOptions{Kind: "dto", Name: "dto-record", Directory: project}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("duplicate resource error = %v", err)
	}

	brokenProject := t.TempDir()
	if err := os.WriteFile(filepath.Join(brokenProject, "internal"), []byte("not a directory"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := generateResource(context.Background(), resourceOptions{Kind: "dto", Name: "broken", Directory: brokenProject}); err == nil || !strings.Contains(err.Error(), "inspect resource package") {
		t.Fatalf("invalid internal directory error = %v", err)
	}
}

func TestResourceFieldParsingAndNames(t *testing.T) {
	defaults, err := parseResourceFields("")
	if err != nil || len(defaults) != 1 || defaults[0].Name != "Name" {
		t.Fatalf("default fields = %#v, err=%v", defaults, err)
	}
	for _, raw := range []string{"missing-type", ":string", "name:", "first-name:string,first_name:string", "value:binary"} {
		if _, err := parseResourceFields(raw); err == nil {
			t.Fatalf("invalid fields %q were accepted", raw)
		}
	}
	withoutName, err := parseResourceFields("amount:decimal")
	if err != nil {
		t.Fatal(err)
	}
	if len(withoutName) != 2 || withoutName[0].Name != "Name" || withoutName[1].Name != "Amount" {
		t.Fatalf("explicit fields without name = %#v, want default Name plus explicit fields", withoutName)
	}

	imports := resourceImports([]field{{GoType: "decimal.Decimal"}, {GoType: "time.Time"}, {GoType: "decimal.Decimal"}, {GoType: "string"}})
	if strings.Count(imports, "decimal") != 1 || !strings.Contains(imports, `"time"`) || strings.Index(imports, "decimal") > strings.Index(imports, "time") {
		t.Fatalf("resource imports are not unique and sorted:\n%s", imports)
	}
	if got := resourceImports([]field{{GoType: "string"}}); got != "" {
		t.Fatalf("string-only imports = %q", got)
	}

	for _, tt := range []struct {
		input       string
		title       string
		packageName string
		route       string
		json        string
	}{
		{input: "order-item", title: "OrderItem", packageName: "orderitem", route: "order-item", json: "order_item"},
		{input: "123 report", title: "Resource123Report", packageName: "resource123report", route: "123-report", json: "123_report"},
		{input: "!!!", title: "Resource", packageName: "resource", route: "resource", json: "field"},
	} {
		if got := titleName(tt.input); got != tt.title {
			t.Fatalf("titleName(%q) = %q", tt.input, got)
		}
		if got := packageName(tt.input); got != tt.packageName {
			t.Fatalf("packageName(%q) = %q", tt.input, got)
		}
		if got := routeName(tt.input); got != tt.route {
			t.Fatalf("routeName(%q) = %q", tt.input, got)
		}
		if got := jsonName(tt.input); got != tt.json {
			t.Fatalf("jsonName(%q) = %q", tt.input, got)
		}
	}
}

func TestResourceTemplatesReturnActionableErrors(t *testing.T) {
	if _, err := executeResourceTemplate("broken", "{{", resourceData{}); err == nil || !strings.Contains(err.Error(), "parse resource template") {
		t.Fatalf("template parse error = %v", err)
	}
	if _, err := executeResourceTemplate("missing", "{{.Missing}}", resourceData{}); err == nil || !strings.Contains(err.Error(), "render resource template") {
		t.Fatalf("template render error = %v", err)
	}
	if contents, err := executeResourceTemplate("ok", "package {{.PackageName}}", resourceData{PackageName: "invoice"}); err != nil || string(contents) != "package invoice" {
		t.Fatalf("template output = %q, err=%v", contents, err)
	}
}

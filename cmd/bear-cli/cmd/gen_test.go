package cmd

import (
	"os"
	"strings"
	"testing"
)

func TestLegacyGenCommandIsThinSharedCLIDelegate(t *testing.T) {
	contents, err := os.ReadFile("gen.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	if !strings.Contains(text, `var genCmd = legacyCommand("gen")`) {
		t.Fatalf("legacy gen command does not delegate to internal/cli:\n%s", text)
	}
	for _, forbidden := range []string{
		"func generateAPI(",
		"const modelTmpl",
		"const controllerTmpl",
		`"text/template"`,
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("legacy gen command retains unreachable implementation %q", forbidden)
		}
	}
}

func TestV091LegacyExportedDataTypesRemainSourceCompatible(t *testing.T) {
	field := Field{
		Name: "Amount", Type: "decimal", GoType: "decimal.Decimal",
		JsonTag: "amount", Validate: "required", GormTag: "type:decimal(10,2)",
	}
	data := TemplateData{
		PackageName: "invoice", Title: "Invoice", Name: "invoice",
		ModuleName: "example.com/invoice", Fields: []Field{field},
		FieldsStr: "amount:decimal", NeedsTime: false,
	}
	if len(data.Fields) != 1 || data.Fields[0].JsonTag != "amount" {
		t.Fatalf("legacy exported data types changed: %#v", data)
	}
	if got := (Field{GoType: "bool"}).GetDefaultValue(); got != "false" {
		t.Fatalf("legacy Field.GetDefaultValue()=%q", got)
	}
}

package gen

import (
	"go/format"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGenerateRendersInjectorRegistration covers the package's whole purpose:
// scan a package, then render the injector registration for it.
//
// Both halves used to be broken. Generate failed on every call because the
// template asked for a ModuleName field that was never supplied, and the scanner
// pasted fmt's debug form of a composite type into the Resolve[T] type argument.
//
// This test asserts the rendered text and its formatting; that the result
// actually compiles is covered by
// TestGeneratedInjectorCompilesInScannedPackage, which builds a fixture module.
// The two used to be one test, and the name promised compilation while the body
// only ran format.Source — which parses without resolving a single identifier.
func TestGenerateRendersInjectorRegistration(t *testing.T) {
	source := "package fixture\n\ntype Repository struct{}\n\ntype Service struct {\n" +
		"\tRepo *Repository `inject:\"\"`\n" +
		"}\n"
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "service.go"), []byte(source), 0644); err != nil {
		t.Fatal(err)
	}
	infos, err := NewScanner(dir).Scan()
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	// The injector names the scanned package's structs unqualified, so it lands
	// in that package. Writing it elsewhere cannot compile.
	output := filepath.Join(dir, "ioc_gen.go")
	if err := NewGenerator("fixture").Generate(infos, output); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	contents, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"github.com/duiniwukenaihe/gin-bear/pkg/bear"`,
		`bear.RegisterRuntimeStaticInjector("Service"`,
		`target.Repo = bear.Resolve[*Repository](factory)`,
	} {
		if !strings.Contains(string(contents), want) {
			t.Fatalf("generated injector missing %q:\n%s", want, contents)
		}
	}
	formatted, err := format.Source(contents)
	if err != nil {
		t.Fatalf("generated injector is not valid Go: %v\n%s", err, contents)
	}
	if string(formatted) != string(contents) {
		t.Fatalf("generated injector is not gofmt formatted:\n%s", contents)
	}
}

// TestGenerateCreatesMissingOutputDirectory keeps the parity between Generate
// and GenerateFromTemplate: both create the parent directory they are given.
func TestGenerateCreatesMissingOutputDirectory(t *testing.T) {
	source := "package fixture\n\ntype Repository struct{}\n\ntype Service struct {\n\tRepo *Repository `inject:\"\"`\n}\n"
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "service.go"), []byte(source), 0644); err != nil {
		t.Fatal(err)
	}
	infos, err := NewScanner(dir).Scan()
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	output := filepath.Join(dir, "nested", "deeper", "ioc_gen.go")
	if err := NewGenerator("fixture").Generate(infos, output); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if _, err := os.Stat(output); err != nil {
		t.Fatalf("Generate() did not create the output directory: %v", err)
	}
}

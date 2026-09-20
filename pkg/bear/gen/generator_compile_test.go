package gen

import (
	"go/format"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// frameworkRootFromGen returns the framework checkout that owns this package, so
// a generated fixture can depend on it through a replace directive.
func frameworkRootFromGen(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("locate framework checkout from %s: %v", root, err)
	}
	return root
}

// newFrameworkModule lays out a module that depends on the framework checkout
// and returns its directory.
//
// The module is deliberately left untidied: nothing imports the framework until
// a generated file exists, and `go mod tidy` would drop the requirement as
// unused if it ran first.
func newFrameworkModule(t *testing.T) string {
	t.Helper()
	project := t.TempDir()
	writeFixtureFile(t, filepath.Join(project, "go.mod"), "module fixture\n\ngo 1.25.14\n\n"+
		"require github.com/duiniwukenaihe/gin-bear v0.0.0\n\n"+
		"replace github.com/duiniwukenaihe/gin-bear => "+frameworkRootFromGen(t)+"\n")
	return project
}

// newInjectorFixture adds the scanned package to a fresh framework module.
func newInjectorFixture(t *testing.T, source string) string {
	t.Helper()
	project := newFrameworkModule(t)
	writeFixtureFile(t, filepath.Join(project, "service.go"), source)
	return project
}

func writeFixtureFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func fixtureGo(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("go", args...)
	command.Dir = dir
	command.Env = append(os.Environ(), "GOSUMDB=sum.golang.org", "GOTOOLCHAIN=go1.26.6")
	combined, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("go %s in fixture: %v\n%s", strings.Join(args, " "), err, combined)
	}
	return string(combined)
}

// TestGeneratedInjectorCompilesInScannedPackage proves the generated file is
// compilable Go, not merely parseable.
//
// The older assertion ran format.Source, which parses but never resolves an
// identifier: it accepts a Resolve[T] whose T does not exist. The injector names
// the scanned package's structs without a qualifier, so the one package it can
// compile in is the package it was scanned from.
func TestGeneratedInjectorCompilesInScannedPackage(t *testing.T) {
	project := newInjectorFixture(t, `package fixture

type Repository struct{}

type Service struct {
	Repo    *Repository    `+"`inject:\"\"`"+`
	Lookup  map[string]int `+"`inject:\"-\"`"+`
	Publish chan struct{}  `+"`inject:\"\"`"+`
}
`)

	infos, err := NewScanner(project).Scan()
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if len(infos) != 1 || infos[0].StructName != "Service" {
		t.Fatalf("Scan() = %+v, want exactly one Service entry", infos)
	}

	// Generating next to the scanned file is what the contract requires, so the
	// generated file lands in the same package.
	if err := NewGenerator("fixture").Generate(infos, filepath.Join(project, "ioc_gen.go")); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	fixtureGo(t, project, "mod", "tidy")
	fixtureGo(t, project, "build", "./...")
}

// TestGeneratedInjectorInAnotherPackageDoesNotCompile records the generator's
// real contract rather than pretending it is layout independent.
//
// NewGenerator receives only a package *name*, so the template cannot qualify
// Repository/Service with an import path; generating into a different package
// leaves those identifiers unresolved. Asserting the failure keeps the limit
// honest — if a later change makes cross-package output work, this test fails and
// the contract has to be re-read instead of silently drifting.
func TestGeneratedInjectorInAnotherPackageDoesNotCompile(t *testing.T) {
	project := newInjectorFixture(t, `package fixture

type Repository struct{}

type Service struct {
	Repo *Repository `+"`inject:\"\"`"+`
}
`)

	infos, err := NewScanner(project).Scan()
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if err := NewGenerator("ioc").Generate(infos, filepath.Join(project, "internal", "ioc", "ioc_gen.go")); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	fixtureGo(t, project, "mod", "tidy")

	command := exec.Command("go", "build", "./...")
	command.Dir = project
	command.Env = append(os.Environ(), "GOSUMDB=sum.golang.org", "GOTOOLCHAIN=go1.26.6")
	combined, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("cross-package injector compiled; the layout limit no longer holds:\n%s", combined)
	}
	if !strings.Contains(string(combined), "undefined: Repository") {
		t.Fatalf("cross-package injector failed for an unexpected reason:\n%s", combined)
	}
}

// TestExportedControllerTemplateRendersCompilablePackage checks the exported
// template the package offers for GenerateFromTemplate. It is public API, so a
// caller can render it into a project as-is, and nothing else verified that the
// result builds.
func TestExportedControllerTemplateRendersCompilablePackage(t *testing.T) {
	project := newFrameworkModule(t)
	path := filepath.Join(project, "controllers", "hello.go")
	if err := NewGenerator("unused").GenerateFromTemplate(ControllerTemplate, struct{ Name string }{Name: "Hello"}, path); err != nil {
		t.Fatalf("GenerateFromTemplate() error = %v", err)
	}
	// GenerateFromTemplate writes the template verbatim, so the constant has to
	// be gofmt clean on its own.
	assertGofmtClean(t, path)
	fixtureGo(t, project, "mod", "tidy")
	fixtureGo(t, project, "build", "./...")
}

// TestExportedServiceTemplateKeepsThePinnedUnusedImport records the wart in the
// other exported template instead of silently repairing it.
//
// ServiceTemplate imports pkg/bear and references nothing from it, so a rendered
// service package fails with "imported and not used". Dropping the import would
// fix the rendering, but the constant's value is part of the pinned v0.9.1 public
// API baseline and scripts/check-api-compat.sh rejects the change:
//
//	./pkg/bear/gen.ServiceTemplate: value changed from "package services\n\nimport (…
//
// The compatibility policy keeps the value as-is, so this test asserts the
// current behaviour. If a later change makes the rendered package compile, this
// test fails and the baseline update has to be deliberate rather than incidental.
func TestExportedServiceTemplateKeepsThePinnedUnusedImport(t *testing.T) {
	project := newFrameworkModule(t)
	path := filepath.Join(project, "services", "hello.go")
	if err := NewGenerator("unused").GenerateFromTemplate(ServiceTemplate, struct{ Name string }{Name: "Hello"}, path); err != nil {
		t.Fatalf("GenerateFromTemplate() error = %v", err)
	}
	assertGofmtClean(t, path)
	fixtureGo(t, project, "mod", "tidy")

	command := exec.Command("go", "build", "./...")
	command.Dir = project
	command.Env = append(os.Environ(), "GOSUMDB=sum.golang.org", "GOTOOLCHAIN=go1.26.6")
	combined, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("ServiceTemplate now renders a compilable package; update the v0.9.1 baseline deliberately:\n%s", combined)
	}
	if !strings.Contains(string(combined), `"github.com/duiniwukenaihe/gin-bear/pkg/bear" imported and not used`) {
		t.Fatalf("ServiceTemplate failed for an unexpected reason:\n%s", combined)
	}
}

func assertGofmtClean(t *testing.T, path string) {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	formatted, err := format.Source(contents)
	if err != nil {
		t.Fatalf("rendered template %s is not valid Go: %v\n%s", path, err, contents)
	}
	if string(formatted) != string(contents) {
		t.Fatalf("rendered template %s is not gofmt formatted:\n%s", path, contents)
	}
}

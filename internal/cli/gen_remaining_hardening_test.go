package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/duiniwukenaihe/gin-bear/internal/scaffold"
	"golang.org/x/mod/modfile"
)

func TestGeneratedAPIHardensPatchAndPersistence(t *testing.T) {
	resource := generateHardeningAPI(t)

	dto := readGeneratedFile(t, resource, "dto.go")
	for _, want := range []string{
		`json:"email" binding:"omitempty,email"`,
		`json:"website" binding:"omitempty,url"`,
		`json:"count" binding:"omitempty,numeric"`,
	} {
		if !strings.Contains(dto, want) {
			t.Fatalf("generated UpdateDTO missing %q:\n%s", want, dto)
		}
	}

	repository := readGeneratedFile(t, resource, "repository.go")
	if !strings.Contains(repository, `return bear.ErrInvalidParams.WithMsg("at least one field is required")`) {
		t.Fatalf("generated repository silently accepts an empty update:\n%s", repository)
	}
	if got := strings.Count(repository, "if result.RowsAffected == 0 {"); got != 2 {
		t.Fatalf("generated repository RowsAffected checks = %d, want 2:\n%s", got, repository)
	}
	if got := strings.Count(repository, "return gorm.ErrRecordNotFound"); got != 2 {
		t.Fatalf("generated repository record-not-found returns = %d, want 2:\n%s", got, repository)
	}
}

func TestGeneratedAPIMapsNotFoundWithoutLeakingStorageErrors(t *testing.T) {
	resource := generateHardeningAPI(t)
	service := readGeneratedFile(t, resource, "service.go")

	for _, want := range []string{
		`errors.Is(err, gorm.ErrRecordNotFound)`,
		`return bear.ErrNotFound.WithErr(err)`,
		`return nil, mapInvoiceServiceError(err)`,
		`return mapInvoiceServiceError(s.Repo.Update(ctx, id, dto))`,
		`return mapInvoiceServiceError(s.Repo.Delete(ctx, id))`,
	} {
		if !strings.Contains(service, want) {
			t.Fatalf("generated service missing %q:\n%s", want, service)
		}
	}
}

func TestGeneratedAPIUsesExplicitStatusesPositiveIDsAndStrictBuilders(t *testing.T) {
	resource := generateHardeningAPI(t)
	controller := readGeneratedFile(t, resource, "controller.go")
	for _, want := range []string{
		`func (c *InvoiceController) BuildE(b *bear.Bear) error`,
		`b.HandleE("POST", "/invoice", c.createWithStatus)`,
		`b.HandleE("PATCH", "/invoice/:id", c.Update)`,
		`b.HandleE("PUT", "/invoice/:id", c.Update)`,
		`bear.WithStatus(http.StatusCreated, response)`,
		`bear.WithStatus(http.StatusNoContent, nil)`,
		`return bear.Success(nil), c.Service.Update(ctx, id, request)`,
		`if id <= 0`,
	} {
		if !strings.Contains(controller, want) {
			t.Fatalf("generated controller missing %q:\n%s", want, controller)
		}
	}
	if !strings.Contains(controller, `if err := c.BuildE(b); err != nil {`) || !strings.Contains(controller, "panic(err)") {
		t.Fatalf("generated controller Build is not a compatibility panic wrapper:\n%s", controller)
	}

	module := readGeneratedFile(t, resource, "module.go")
	for _, want := range []string{
		`func (m *Module) BuildE(b *bear.Bear) error`,
		`return b.MountE("/api/v1", m.controller)`,
		`b.Mount("/api/v1", m.controller)`,
	} {
		if !strings.Contains(module, want) {
			t.Fatalf("generated module missing %q:\n%s", want, module)
		}
	}
}

func TestGeneratedHardenedAPICompiles(t *testing.T) {
	resource := generateHardeningAPI(t)
	project := filepath.Dir(filepath.Dir(resource))
	writeGeneratedTestGoMod(t, project, "example.com/generated-hardening")
	compileGeneratedPackages(t, project, "./internal/invoice")
}

func TestGeneratedAPIRegistersManagedManifestAndStableRegistry(t *testing.T) {
	project := newManagedGenerationProject(t)
	for _, name := range []string{"zeta", "alpha"} {
		if _, err := generateResource(context.Background(), resourceOptions{
			Kind: "api", Name: name, Directory: project,
		}); err != nil {
			t.Fatalf("generate %s: %v", name, err)
		}
	}

	manifest, err := scaffold.ReadManifest(project)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.APIs) != 2 {
		t.Fatalf("manifest APIs = %#v", manifest.APIs)
	}
	for index, want := range []scaffold.GeneratedAPI{
		{Name: "Alpha", Package: "alpha", Path: "internal/alpha", ModuleType: "alpha.Module"},
		{Name: "Zeta", Package: "zeta", Path: "internal/zeta", ModuleType: "zeta.Module"},
	} {
		if manifest.APIs[index] != want {
			t.Fatalf("manifest API %d = %#v, want %#v", index, manifest.APIs[index], want)
		}
	}

	registry := readGeneratedFile(t, project, filepath.FromSlash(scaffold.ModulesPath))
	for _, want := range []string{
		`alpha "example.com/managed/internal/alpha"`,
		`zeta "example.com/managed/internal/zeta"`,
		"alpha.NewModule()",
		"zeta.NewModule()",
	} {
		if !strings.Contains(registry, want) {
			t.Fatalf("generated registry missing %q:\n%s", want, registry)
		}
	}
	if strings.Index(registry, "alpha.NewModule()") > strings.Index(registry, "zeta.NewModule()") {
		t.Fatalf("generated registry is not stably sorted:\n%s", registry)
	}

	module := readGeneratedFile(t, filepath.Join(project, "internal", "alpha"), "module.go")
	for _, want := range []string{
		"type Module struct {",
		"type AlphaModule = Module",
		"func NewModule() *Module",
		"repository := &AlphaRepository{}",
		"service := &AlphaService{Repo: repository}",
		"controller := &AlphaController{Service: service}",
	} {
		if !strings.Contains(module, want) {
			t.Fatalf("generated module missing %q:\n%s", want, module)
		}
	}
	assertGenerationLockAbsent(t, project)
	compileGeneratedPackages(t, project, "./internal/app")
}

func TestGeneratedAPIHonorsExistingManifestLock(t *testing.T) {
	project := newManagedGenerationProject(t)
	lockPath := filepath.Join(project, ".bear", "generate.lock")
	if err := os.WriteFile(lockPath, []byte("held"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := generateResource(context.Background(), resourceOptions{
		Kind: "api", Name: "invoice", Directory: project,
	})
	if err == nil || !strings.Contains(err.Error(), "generation lock") {
		t.Fatalf("generation lock error = %v", err)
	}
	if contents, readErr := os.ReadFile(lockPath); readErr != nil || string(contents) != "held" {
		t.Fatalf("existing generation lock changed: contents=%q err=%v", contents, readErr)
	}
	if _, statErr := os.Stat(filepath.Join(project, "internal", "invoice")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("locked generation published resource: %v", statErr)
	}
}

func TestGeneratedAPIRegistrationFailureRollsBackResourceAndMetadata(t *testing.T) {
	project := newManagedGenerationProject(t)
	manifestPath := filepath.Join(project, filepath.FromSlash(scaffold.ManifestPath))
	registryPath := filepath.Join(project, filepath.FromSlash(scaffold.ModulesPath))
	manifestBefore, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	registryBefore, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	appDirectory := filepath.Dir(registryPath)
	if err := os.Chmod(appDirectory, 0555); err != nil {
		t.Fatal(err)
	}
	_, generateErr := generateResource(context.Background(), resourceOptions{
		Kind: "api", Name: "invoice", Directory: project,
	})
	if err := os.Chmod(appDirectory, 0755); err != nil {
		t.Fatal(err)
	}
	if generateErr == nil || !strings.Contains(generateErr.Error(), "module registry") {
		t.Fatalf("registration failure = %v", generateErr)
	}
	if _, statErr := os.Stat(filepath.Join(project, "internal", "invoice")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed registration kept generated resource: %v", statErr)
	}
	manifestAfter, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	registryAfter, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(manifestAfter, manifestBefore) || !bytes.Equal(registryAfter, registryBefore) {
		t.Fatalf("failed registration changed metadata\nmanifest before=%safter=%s\nregistry before=%safter=%s", manifestBefore, manifestAfter, registryBefore, registryAfter)
	}
	assertGenerationLockAbsent(t, project)
}

func TestGeneratedAPILegacyProjectPrintsManualRegistrationHint(t *testing.T) {
	project := t.TempDir()
	t.Chdir(project)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := Execute([]string{"gen", "api", "invoice"}, &stdout, &stderr); code != 0 {
		t.Fatalf("gen exit code = %d, stderr=%s", code, stderr.String())
	}
	for _, want := range []string{"Generated internal/invoice", "invoice.NewModule()", "AddModule"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("legacy generation output missing %q: %s", want, stdout.String())
		}
	}
	if _, err := os.Stat(filepath.Join(project, filepath.FromSlash(scaffold.ModulesPath))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy generation wrote managed registry: %v", err)
	}
}

func TestGenCommandUsesNearestGoModRootFromSubdirectory(t *testing.T) {
	for _, managed := range []bool{false, true} {
		name := "legacy"
		if managed {
			name = "managed"
		}
		t.Run(name, func(t *testing.T) {
			var project string
			if managed {
				project = newManagedGenerationProject(t)
			} else {
				project = t.TempDir()
				writeGeneratedTestGoMod(t, project, "example.com/legacy")
			}
			nested := filepath.Join(project, "tools", "deep")
			if err := os.MkdirAll(nested, 0755); err != nil {
				t.Fatal(err)
			}
			t.Chdir(nested)
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			resourceName := name + "-invoice"
			if code := Execute([]string{"gen", "api", resourceName}, &stdout, &stderr); code != 0 {
				t.Fatalf("gen exit code = %d, stderr=%s", code, stderr.String())
			}
			packageName := packageName(resourceName)
			if _, err := os.Stat(filepath.Join(project, "internal", packageName, "module.go")); err != nil {
				t.Fatalf("project-root resource missing: %v", err)
			}
			if _, err := os.Stat(filepath.Join(nested, "internal")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("nested execution wrote nested internal directory: %v", err)
			}
			if managed {
				manifest, err := scaffold.ReadManifest(project)
				if err != nil {
					t.Fatal(err)
				}
				if len(manifest.APIs) != 1 || manifest.APIs[0].Package != packageName {
					t.Fatalf("managed subdirectory generation manifest = %#v", manifest.APIs)
				}
			} else if !strings.Contains(stdout.String(), "AddModule") {
				t.Fatalf("legacy subdirectory generation output lacks manual hint: %s", stdout.String())
			}
		})
	}
}

func TestGeneratedAPIAddsDirectRuntimeDependencies(t *testing.T) {
	project := t.TempDir()
	writeGeneratedTestGoMod(t, project, "example.com/direct-dependencies")
	if _, err := generateResource(context.Background(), resourceOptions{
		Kind: "api", Name: "invoice", Directory: project,
	}); err != nil {
		t.Fatal(err)
	}

	contents, err := os.ReadFile(filepath.Join(project, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	file, err := modfile.Parse("go.mod", contents, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"github.com/gin-gonic/gin": "v1.12.0",
		"gorm.io/gorm":             "v1.26.0",
	}
	for _, requirement := range file.Require {
		version, exists := want[requirement.Mod.Path]
		if !exists {
			continue
		}
		if requirement.Mod.Version != version || requirement.Indirect {
			t.Fatalf("requirement %s = %s indirect=%v, want direct %s", requirement.Mod.Path, requirement.Mod.Version, requirement.Indirect, version)
		}
		delete(want, requirement.Mod.Path)
	}
	if len(want) != 0 {
		t.Fatalf("generated API missing direct requirements: %#v\ngo.mod:\n%s", want, contents)
	}
}

func TestEnsureDirectRequirementPreservesHigherVersion(t *testing.T) {
	file, err := modfile.Parse("go.mod", []byte("module example.com/service\n\nrequire gorm.io/gorm v1.30.0 // indirect\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := ensureDirectRequirement(file, "gorm.io/gorm", gormModuleVersion)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("ensureDirectRequirement did not promote indirect dependency")
	}
	for _, requirement := range file.Require {
		if requirement.Mod.Path != "gorm.io/gorm" {
			continue
		}
		if requirement.Mod.Version != "v1.30.0" || requirement.Indirect {
			t.Fatalf("gorm requirement = %s indirect=%v", requirement.Mod.Version, requirement.Indirect)
		}
		return
	}
	t.Fatal("gorm requirement missing")
}

func generateHardeningAPI(t *testing.T) string {
	t.Helper()
	project := t.TempDir()
	if _, err := generateResource(context.Background(), resourceOptions{
		Kind:      "api",
		Name:      "invoice",
		Fields:    "email:email,website:url,count:int",
		Directory: project,
	}); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(project, "internal", "invoice")
}

func readGeneratedFile(t *testing.T, resource, name string) string {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(resource, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}

func newManagedGenerationProject(t *testing.T) string {
	t.Helper()
	project := t.TempDir()
	writeGeneratedTestGoMod(t, project, "example.com/managed")
	if err := scaffold.WriteManifest(project, scaffold.NewManifest("example.com/managed", "v0.9.2")); err != nil {
		t.Fatal(err)
	}
	registryPath := filepath.Join(project, filepath.FromSlash(scaffold.ModulesPath))
	if err := os.MkdirAll(filepath.Dir(registryPath), 0755); err != nil {
		t.Fatal(err)
	}
	registry := "// Code generated by bear. DO NOT EDIT.\n\npackage app\n\n" +
		"import \"github.com/duiniwukenaihe/gin-bear/pkg/bear\"\n\n" +
		"func generatedModules() []bear.Module { return nil }\n"
	if err := os.WriteFile(registryPath, []byte(registry), 0644); err != nil {
		t.Fatal(err)
	}
	return project
}

func writeGeneratedTestGoMod(t *testing.T, project, module string) {
	t.Helper()
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	goMod := "module " + module + "\n\ngo 1.25.12\n\n" +
		"require github.com/duiniwukenaihe/gin-bear v0.0.0\n\n" +
		"replace github.com/duiniwukenaihe/gin-bear => " + repositoryRoot + "\n"
	if err := os.WriteFile(filepath.Join(project, "go.mod"), []byte(goMod), 0644); err != nil {
		t.Fatal(err)
	}
}

func compileGeneratedPackages(t *testing.T, project string, packages ...string) {
	t.Helper()
	goBinary, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	args := append([]string{"test", "-mod=mod", "-run", "^$"}, packages...)
	command := exec.Command(goBinary, args...)
	command.Dir = project
	command.Env = append(os.Environ(), "GOWORK=off")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("compile generated packages: %v\n%s", err, output)
	}
}

func assertGenerationLockAbsent(t *testing.T, project string) {
	t.Helper()
	lockPath := filepath.Join(project, ".bear", "generate.lock")
	if _, err := os.Stat(lockPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("generation lock remains: %v", err)
	}
}

package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/duiniwukenaihe/gin-bear/pkg/bear"
)

func TestDefaultFrameworkVersionRequiresExplicitReleaseForDevelopmentBuild(t *testing.T) {
	previous := bear.Version
	t.Cleanup(func() { bear.Version = previous })

	bear.Version = "dev"
	if got := defaultFrameworkVersion(); got != "" {
		t.Fatalf("defaultFrameworkVersion() = %q, want no implicit incompatible release", got)
	}
}

func TestNewCommandRequiresFrameworkVersionInDevelopmentBuild(t *testing.T) {
	previous := bear.Version
	t.Cleanup(func() { bear.Version = previous })
	bear.Version = "dev"

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Execute([]string{"new", "example.com/service", "--directory", t.TempDir() + "/service"}, &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "--framework-version") {
		t.Fatalf("Execute() code = %d stderr = %q", code, stderr.String())
	}
}

func TestDevelopmentNewRejectsPublishedFrameworkBeforeGeneration(t *testing.T) {
	previous := bear.Version
	t.Cleanup(func() { bear.Version = previous })
	bear.Version = "dev"

	destination := filepath.Join(t.TempDir(), "service")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Execute([]string{
		"new", "service",
		"--module", "example.com/service",
		"--directory", destination,
		"--framework-version", "v0.9.3",
	}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("Execute() code = %d, want 1; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	for _, want := range []string{"development generator", "v0.9.3", "unreleased HEAD", "--framework-version dev", "--framework-replace"} {
		if !strings.Contains(stderr.String(), want) {
			t.Fatalf("development mismatch error missing %q: %q", want, stderr.String())
		}
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("version mismatch published destination: %v", err)
	}
}

func TestDevelopmentNewRequiresExplicitLocalReplace(t *testing.T) {
	previous := bear.Version
	t.Cleanup(func() { bear.Version = previous })
	bear.Version = "dev"

	destination := filepath.Join(t.TempDir(), "service")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Execute([]string{
		"new", "service",
		"--module", "example.com/service",
		"--directory", destination,
		"--framework-version", "dev",
	}, &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "--framework-replace") {
		t.Fatalf("Execute() code = %d stderr = %q, want explicit local replace error", code, stderr.String())
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("missing replace published destination: %v", err)
	}
}

func TestDevelopmentNewPinsPlaceholderAndLocalReplace(t *testing.T) {
	previous := bear.Version
	t.Cleanup(func() { bear.Version = previous })
	bear.Version = "dev"

	repository := repositoryRoot(t)
	destination := filepath.Join(t.TempDir(), "service")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Execute([]string{
		"new", "service",
		"--module", "example.com/service",
		"--directory", destination,
		"--framework-version", "dev",
		"--framework-replace", repository,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Execute() code = %d, stderr=%q", code, stderr.String())
	}
	goMod, err := os.ReadFile(filepath.Join(destination, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"github.com/duiniwukenaihe/gin-bear v0.0.0",
		"replace github.com/duiniwukenaihe/gin-bear => " + repository,
	} {
		if !strings.Contains(string(goMod), want) {
			t.Fatalf("development go.mod missing %q:\n%s", want, goMod)
		}
	}
}

func TestReleasedNewRejectsDifferentFrameworkVersion(t *testing.T) {
	previous := bear.Version
	t.Cleanup(func() { bear.Version = previous })
	bear.Version = "v0.10.0"

	destination := filepath.Join(t.TempDir(), "service")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Execute([]string{
		"new", "service",
		"--module", "example.com/service",
		"--directory", destination,
		"--framework-version", "v0.9.3",
	}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("Execute() code = %d, want 1; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	for _, want := range []string{"templates target v0.10.0", "v0.9.3", "must match"} {
		if !strings.Contains(stderr.String(), want) {
			t.Fatalf("release mismatch error missing %q: %q", want, stderr.String())
		}
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("release mismatch published destination: %v", err)
	}
}

func TestDefaultFrameworkVersionUsesReleaseBuildVersion(t *testing.T) {
	previous := bear.Version
	t.Cleanup(func() { bear.Version = previous })

	bear.Version = "0.9.2"
	if got := defaultFrameworkVersion(); got != "v0.9.2" {
		t.Fatalf("defaultFrameworkVersion() = %q, want release build version v0.9.2", got)
	}
}

func TestResolveFrameworkVersionUsesGoModuleVersionWithoutLinkerFlags(t *testing.T) {
	if got := resolveFrameworkVersion("dev", "v1.2.3"); got != "v1.2.3" {
		t.Fatalf("resolveFrameworkVersion() = %q, want v1.2.3", got)
	}
	if got := resolveFrameworkVersion("dev", "v0.10.0-rc.2"); got != "v0.10.0-rc.2" {
		t.Fatalf("resolveFrameworkVersion() = %q, want formal prerelease tag", got)
	}
	if got := resolveFrameworkVersion("dev", "v0.9.4-0.20260812160339-d82817f9b633"); got != "" {
		t.Fatalf("resolveFrameworkVersion() = %q, want pseudo-version to remain a development build", got)
	}
	if got := resolveFrameworkVersion("dev", "v0.9.4-0.20260812160339-d82817f9b633+dirty"); got != "" {
		t.Fatalf("resolveFrameworkVersion() = %q, want dirty pseudo-version to remain a development build", got)
	}
	if got := resolveFrameworkVersion("dev", "release-candidate"); got != "" {
		t.Fatalf("resolveFrameworkVersion() = %q, want non-semver build label to remain a development build", got)
	}
	if got := resolveFrameworkVersion("dev", "(devel)"); got != "" {
		t.Fatalf("resolveFrameworkVersion() = %q, want development build to require an explicit version", got)
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve CLI test source path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
}

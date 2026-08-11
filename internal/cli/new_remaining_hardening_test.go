package cli

import (
	"bytes"
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

func TestDefaultFrameworkVersionUsesReleaseBuildVersion(t *testing.T) {
	previous := bear.Version
	t.Cleanup(func() { bear.Version = previous })

	bear.Version = "0.9.2"
	if got := defaultFrameworkVersion(); got != "v0.9.2" {
		t.Fatalf("defaultFrameworkVersion() = %q, want release build version v0.9.2", got)
	}
}

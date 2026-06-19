package scripts

import (
	"os"
	"strings"
	"testing"
)

func TestReleaseCheckScriptCoversProductionGates(t *testing.T) {
	info, err := os.Stat("release-check.sh")
	if err != nil {
		t.Fatalf("release-check.sh should exist: %v", err)
	}
	if info.Mode()&0111 == 0 {
		t.Fatalf("release-check.sh should be executable, mode=%s", info.Mode())
	}
	content, err := os.ReadFile("release-check.sh")
	if err != nil {
		t.Fatalf("read release-check.sh: %v", err)
	}
	text := string(content)
	for _, want := range []string{
		"go build ./cmd ./cmd/bear ./cmd/bear-cli",
		"go test ./... -count=1",
		"go test -race ./... -count=1",
		"go vet ./...",
		"govulncheck",
		"go mod tidy",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("release-check.sh missing %q:\n%s", want, text)
		}
	}
	for _, unwanted := range []string{
		"syft",
		"GENERATE_SBOM",
		"sbom.spdx.json",
	} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("release-check.sh should focus on Go framework checks and not contain %q:\n%s", unwanted, text)
		}
	}
}

func TestCIInvokesFrameworkReleaseCheckOnly(t *testing.T) {
	content, err := os.ReadFile("../.github/workflows/ci.yml")
	if err != nil {
		t.Fatalf("read ci workflow: %v", err)
	}
	text := string(content)
	if !strings.Contains(text, "scripts/release-check.sh") {
		t.Fatalf("CI should invoke release-check.sh:\n%s", text)
	}
	for _, unwanted := range []string{
		"docker build",
		"GENERATE_SBOM",
		"sbom.spdx.json",
		"actions/upload-artifact",
	} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("CI should not contain container delivery step %q:\n%s", unwanted, text)
		}
	}
}

func TestDependabotCoversProductionDependencies(t *testing.T) {
	content, err := os.ReadFile("../.github/dependabot.yml")
	if err != nil {
		t.Fatalf("dependabot config should exist: %v", err)
	}
	text := string(content)
	for _, want := range []string{
		`package-ecosystem: "gomod"`,
		`package-ecosystem: "github-actions"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("dependabot config missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, `package-ecosystem: "docker"`) {
		t.Fatalf("dependabot config should not include docker ecosystem:\n%s", text)
	}
}

func TestSecurityPolicyAndRunbookCoverOperations(t *testing.T) {
	security, err := os.ReadFile("../SECURITY.md")
	if err != nil {
		t.Fatalf("SECURITY.md should exist: %v", err)
	}
	for _, want := range []string{"Supported Versions", "Reporting a Vulnerability", "Security Updates"} {
		if !strings.Contains(string(security), want) {
			t.Fatalf("SECURITY.md missing %q:\n%s", want, string(security))
		}
	}

	runbook, err := os.ReadFile("../docs/runbook.md")
	if err != nil {
		t.Fatalf("docs/runbook.md should exist: %v", err)
	}
	for _, want := range []string{"Release Checklist", "Rollback", "Migration Recovery", "Observability", "Incident Response"} {
		if !strings.Contains(string(runbook), want) {
			t.Fatalf("runbook missing %q:\n%s", want, string(runbook))
		}
	}
}

func TestRepositoryFocusesOnFrameworkNotContainerDelivery(t *testing.T) {
	for _, path := range []string{
		"../Dockerfile",
		"../docker-compose.yml",
		"../.dockerignore",
		"../deploy/kubernetes/deployment.yaml",
		"../deploy/prometheus/rules.yaml",
	} {
		if _, err := os.Stat(path); err == nil {
			t.Fatalf("container delivery artifact should not exist: %s", path)
		} else if !os.IsNotExist(err) {
			t.Fatalf("stat %s: %v", path, err)
		}
	}
}

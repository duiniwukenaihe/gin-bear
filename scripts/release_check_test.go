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
		"syft",
		"GENERATE_SBOM",
		"go install github.com/anchore/syft/cmd/syft@latest",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("release-check.sh missing %q:\n%s", want, text)
		}
	}
}

func TestDockerignoreExcludesNonRuntimeBuildContext(t *testing.T) {
	content, err := os.ReadFile("../.dockerignore")
	if err != nil {
		t.Fatalf(".dockerignore should exist: %v", err)
	}
	text := string(content)
	for _, want := range []string{
		".git",
		".github",
		"docs/superpowers",
		"scripts",
		"*_test.go",
		"sbom.spdx.json",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf(".dockerignore missing %q:\n%s", want, text)
		}
	}
}

func TestRootDockerfileIncludesOCILabelsAndHealthcheck(t *testing.T) {
	content, err := os.ReadFile("../Dockerfile")
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}
	text := string(content)
	for _, want := range []string{
		"org.opencontainers.image.version=${VERSION}",
		"org.opencontainers.image.revision=${COMMIT}",
		"org.opencontainers.image.created=${BUILD_TIME}",
		"HEALTHCHECK",
		"/live",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("Dockerfile missing %q:\n%s", want, text)
		}
	}
}

func TestCIInvokesReleaseCheckScriptAndUploadsSBOM(t *testing.T) {
	content, err := os.ReadFile("../.github/workflows/ci.yml")
	if err != nil {
		t.Fatalf("read ci workflow: %v", err)
	}
	text := string(content)
	for _, want := range []string{
		"GENERATE_SBOM: \"true\"",
		"scripts/release-check.sh",
		"actions/upload-artifact",
		"sbom.spdx.json",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("CI missing %q:\n%s", want, text)
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
		`package-ecosystem: "docker"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("dependabot config missing %q:\n%s", want, text)
		}
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

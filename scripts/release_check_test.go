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
		`go test ./... -count=1 -coverprofile="${coverage_profile}"`,
		`BEAR_RELEASE_E2E=1 go test ./scripts/releasee2e -run '^TestReleaseCandidateApplications$' -count=1`,
		"go test -race ./... -count=1",
		"go vet ./...",
		"govulncheck",
		"go mod tidy",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("release-check.sh missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "coverage_packages") {
		t.Fatalf("release-check.sh must measure total repository coverage, not a package subset:\n%s", text)
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

func TestCIInvokesQualityEntryPointAndSeparateRaceCheck(t *testing.T) {
	content, err := os.ReadFile("../.github/workflows/ci.yml")
	if err != nil {
		t.Fatalf("read ci workflow: %v", err)
	}
	text := string(content)
	for _, want := range []string{
		"run: make verify",
		"run: go test -race ./... -count=1",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("CI missing %q:\n%s", want, text)
		}
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

func TestRepositoryDependencyChecksDoNotCreateUpdateBranches(t *testing.T) {
	content, err := os.ReadFile("release-check.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	for _, want := range []string{
		"go mod tidy",
		"govulncheck@v1.6.0",
		"staticcheck@v0.7.0",
		"check-coverage.sh",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("release check missing %q", want)
		}
	}
	if _, err := os.Stat("../.github/dependabot.yml"); !os.IsNotExist(err) {
		t.Fatalf("dependabot config must remain absent: %v", err)
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

	production, err := os.ReadFile("../docs/production.md")
	if err != nil {
		t.Fatalf("docs/production.md should exist: %v", err)
	}
	for _, want := range []string{"server.shutdown_timeout", "health.readiness_timeout", "log.level", "redis.required", "bear gen api"} {
		if !strings.Contains(string(production), want) {
			t.Fatalf("production docs missing %q:\n%s", want, string(production))
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

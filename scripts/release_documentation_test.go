package scripts

import (
	"os"
	"strings"
	"testing"
)

func TestReleaseDocumentationNamesEveryCompatibilityChange(t *testing.T) {
	text := readDocumentationFile(t, "../docs/migration-v0.9-to-v0.10.md")
	for _, phrase := range []string{
		"strict production configuration",
		"trusted proxies",
		"request body limit",
		"MySQL TLS",
		"metrics",
		"token revocation",
		"Rollback",
	} {
		if !strings.Contains(text, phrase) {
			t.Fatalf("migration guide missing %q", phrase)
		}
	}
}

func TestReadmeUsesTestedExamplesAndCanonicalCLInstallPath(t *testing.T) {
	text := readDocumentationFile(t, "../README.md")
	for _, phrase := range []string{
		"examples/basic/main.go",
		"examples/auth/main.go",
		"examples/migration/main.go",
		"go install github.com/duiniwukenaihe/gin-bear/cmd/bear@v0.10.0",
		"go test ./...",
	} {
		if !strings.Contains(text, phrase) {
			t.Fatalf("README missing %q", phrase)
		}
	}
}

func TestCLIReleaseConfigurationStaysArchiveOnly(t *testing.T) {
	config := readDocumentationFile(t, "../.goreleaser.yml")
	for _, phrase := range []string{
		"version: 2",
		"main: ./cmd/bear",
		"- linux",
		"- darwin",
		"- windows",
		"- amd64",
		"- arm64",
		"algorithm: sha256",
		"source:",
		"include_meta: true",
	} {
		if !strings.Contains(config, phrase) {
			t.Fatalf("GoReleaser configuration missing %q", phrase)
		}
	}
	for _, unwanted := range []string{"dockers:", "docker_manifests:", "docker_digest:", "kos:", "nfpms:"} {
		if strings.Contains(config, unwanted) {
			t.Fatalf("GoReleaser configuration must not publish %q", unwanted)
		}
	}
}

func TestReleaseWorkflowIsTagScopedAndUsesPinnedGoReleaser(t *testing.T) {
	workflow := readDocumentationFile(t, "../.github/workflows/release.yml")
	for _, phrase := range []string{
		"tags:",
		"- \"v*\"",
		"contents: read",
		"contents: write",
		"run: make verify",
		"goreleaser/goreleaser-action@v7",
		"version: v2.17.0",
		"release --clean",
	} {
		if !strings.Contains(workflow, phrase) {
			t.Fatalf("release workflow missing %q", phrase)
		}
	}
	for _, unwanted := range []string{"docker", "container", "registry", "attestations: write", "id-token: write"} {
		if strings.Contains(strings.ToLower(workflow), unwanted) {
			t.Fatalf("release workflow must remain CLI-only and permissions-scoped; found %q", unwanted)
		}
	}
}

func readDocumentationFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

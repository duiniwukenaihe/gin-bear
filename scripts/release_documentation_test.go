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

func TestMigrationMetricsGuidanceMatchesConfigurationAndCompatibilityBehavior(t *testing.T) {
	text := readDocumentationFile(t, "../docs/migration-v0.9-to-v0.10.md")
	normalized := strings.Join(strings.Fields(text), " ")
	for _, phrase := range []string{
		"Newly generated `application-prod.yaml.example` files set `metrics.enabled: false`",
		"`NewSysConfig()` still enables metrics in its in-memory default configuration",
		"On its first call, `EnableMetrics()` skips registration only when the supplied configuration explicitly sets `metrics.enabled: false`",
		"Later calls are idempotent",
		"`EnableHealth()` calls `EnableMetrics()` when metrics configuration is absent or enabled",
	} {
		if !strings.Contains(normalized, phrase) {
			t.Fatalf("migration metrics guidance missing %q", phrase)
		}
	}
	if strings.Contains(normalized, "metrics are disabled unless `metrics.enabled: true` is set") {
		t.Fatal("migration guide must not describe disabled metrics as a global runtime default")
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

func TestReleaseWorkflowIsTagScopedAndUsesImmutableActions(t *testing.T) {
	workflow := readDocumentationFile(t, "../.github/workflows/release.yml")
	for _, phrase := range []string{
		"tags:",
		"- \"v*\"",
		"contents: read",
		"contents: write",
		"actions/checkout@34e114876b0b11c390a56381ad16ebd13914f8d5 # v4.3.1",
		"actions/setup-go@40f1582b2485089dde7abd97c1529aa768e1baff # v5.6.0",
		"goreleaser/goreleaser-action@f06c13b6b1a9625abc9e6e439d9c05a8f2190e94 # v7.2.3",
		"go-version: \"1.25.12\"",
		"run: make verify",
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

func TestRunbookUsesPinnedVerificationAndChecksReleaseChecksums(t *testing.T) {
	runbook := readDocumentationFile(t, "../docs/runbook.md")
	for _, phrase := range []string{
		"GOSUMDB=sum.golang.org GOTOOLCHAIN=go1.25.12 make verify",
		"GOSUMDB=sum.golang.org GOTOOLCHAIN=go1.25.12 go run github.com/goreleaser/goreleaser/v2@v2.17.0 release --snapshot --clean",
		"(cd dist && shasum -a 256 -c checksums.txt)",
	} {
		if !strings.Contains(runbook, phrase) {
			t.Fatalf("runbook missing %q", phrase)
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

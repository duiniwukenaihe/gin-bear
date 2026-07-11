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

func TestMigrationDocumentsComparableCollectionFieldSourceChange(t *testing.T) {
	text := strings.Join(strings.Fields(readDocumentationFile(t, "../docs/migration-v0.9-to-v0.10.md")), " ")
	for _, phrase := range []string{
		"`AuthConfig.PublicPaths`",
		"`WebSocketConfig.AllowedOrigins`",
		"`[]string` to `*[]string`",
		"`SetPublicPaths` and `GetPublicPaths`",
		"`SetAllowedOrigins` and `GetAllowedOrigins`",
		"pointer identity",
		"v0.9 struct comparability",
	} {
		if !strings.Contains(text, phrase) {
			t.Fatalf("migration collection guidance missing %q", phrase)
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

func TestReleaseDocumentationSeparatesPublishedAndUpcomingVersions(t *testing.T) {
	readme := readDocumentationFile(t, "../README.md")
	security := readDocumentationFile(t, "../SECURITY.md")
	normalizedReadme := strings.ToLower(readme)
	for _, phrase := range []string{
		"go install github.com/duiniwukenaihe/gin-bear/cmd/bear@v0.9.1",
		"v0.10.0-rc.1",
		"after publication",
	} {
		if !strings.Contains(normalizedReadme, strings.ToLower(phrase)) {
			t.Fatalf("README missing publication-state guidance %q", phrase)
		}
	}
	if strings.Contains(readme, "go install github.com/duiniwukenaihe/gin-bear/cmd/bear@v0.10.0\n") {
		t.Fatal("README must not present the unpublished release candidate as currently installable")
	}
	for _, phrase := range []string{"v0.9.1", "current", "v0.10", "upcoming", "unreleased"} {
		if !strings.Contains(strings.ToLower(security), strings.ToLower(phrase)) {
			t.Fatalf("SECURITY.md missing publication-state guidance %q", phrase)
		}
	}
}

func TestChangelogKeepsV010ReleaseCandidateUnreleased(t *testing.T) {
	changelog := readDocumentationFile(t, "../CHANGELOG.md")
	const expectedHeading = "## [v0.10.0-rc.1] - Unreleased"
	if !strings.Contains(changelog, expectedHeading) {
		t.Fatalf("CHANGELOG.md missing unreleased release-candidate heading %q", expectedHeading)
	}
	if strings.Contains(changelog, "## [0.10.0] - 2026-07-11") {
		t.Fatal("CHANGELOG.md must not claim the v0.10 release candidate is dated or published")
	}
}

func TestProductionDocumentationUsesPinnedVerifyCommand(t *testing.T) {
	text := readDocumentationFile(t, "../docs/production.md")
	const command = "GOSUMDB=sum.golang.org GOTOOLCHAIN=go1.25.12 make verify"
	if !strings.Contains(text, command) {
		t.Fatalf("production documentation missing %q", command)
	}
	if strings.Contains(text, "GOPROXY=https://goproxy.cn,direct make verify") {
		t.Fatal("production documentation must use the project-pinned verify command")
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
		"actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02 # v4.6.2",
		"goreleaser/goreleaser-action@f06c13b6b1a9625abc9e6e439d9c05a8f2190e94 # v7.2.3",
		"go-version: \"1.25.12\"",
		"run: make verify-rc",
		"name: rc-verification",
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
	if strings.Contains(workflow, "API_BASELINE_REBUILD") {
		t.Fatal("release workflow must not require API baseline reconstruction")
	}
}

func TestRunbookUsesPinnedVerificationAndChecksReleaseChecksums(t *testing.T) {
	runbook := readDocumentationFile(t, "../docs/runbook.md")
	for _, phrase := range []string{
		"GOSUMDB=sum.golang.org GOTOOLCHAIN=go1.25.12 make verify",
		"SHUFFLE_SEED=20260711 STATICCHECK_BIN=/opt/gin-bear/bin/staticcheck GOVULNCHECK_BIN=/opt/gin-bear/bin/govulncheck GOVULNCHECK_DB=file:///opt/gin-bear/vulndb APIDIFF_BIN=/opt/gin-bear/bin/apidiff APIDIFF_EXPECTED_SHA256=84b7e058a4df23bc0e21d3eae07dedc0b93cee85b40ee8c65701944eed5f742f make verify-rc",
		"GOSUMDB=sum.golang.org GOTOOLCHAIN=go1.25.12 go run github.com/goreleaser/goreleaser/v2@v2.17.0 release --snapshot --clean",
		"(cd dist && shasum -a 256 -c checksums.txt)",
	} {
		if !strings.Contains(runbook, phrase) {
			t.Fatalf("runbook missing %q", phrase)
		}
	}
}

func TestReleaseCandidateResultIsAuditableAndStillUnpublished(t *testing.T) {
	runbook := readDocumentationFile(t, "../docs/runbook.md")
	for _, phrase := range []string{
		"v0.10.0-rc.1",
		"75.8%",
		"critical coverage handler 82.9%",
		"critical coverage binding 88.5%",
		"critical coverage lifecycle 84.9%",
		"scripts/check-api-compat.sh",
		"SHUFFLE_SEED=20260711",
		"BEAR_RELEASE_E2E=1 go test ./scripts/releasee2e",
		"not tagged or published",
	} {
		if !strings.Contains(runbook, phrase) {
			t.Fatalf("runbook missing RC audit evidence %q", phrase)
		}
	}

	changelog := readDocumentationFile(t, "../CHANGELOG.md")
	for _, phrase := range []string{"local release-candidate verification", "awaits human review", "not published"} {
		if !strings.Contains(strings.ToLower(changelog), phrase) {
			t.Fatalf("changelog missing RC publication state %q", phrase)
		}
	}
}

func TestHistoricalDirtyRunIsNotPresentedAsCurrentCommitEvidence(t *testing.T) {
	runbook := strings.Join(strings.Fields(readDocumentationFile(t, "../docs/runbook.md")), " ")
	changelog := strings.Join(strings.Fields(readDocumentationFile(t, "../CHANGELOG.md")), " ")
	for name, text := range map[string]string{"runbook": runbook, "changelog": changelog} {
		for _, phrase := range []string{"development-time validation", "dirty worktree", "not evidence for the current commit"} {
			if !strings.Contains(strings.ToLower(text), phrase) {
				t.Fatalf("%s does not classify historical run as %q", name, phrase)
			}
		}
	}
	for _, forbidden := range []string{
		"The fresh local RC run based on commit `1db2743e3b1146ecc6592e0ea46cfa4e5ad311c1`",
		"The final fresh run used base HEAD `1db2743e3b1146ecc6592e0ea46cfa4e5ad311c1`",
	} {
		if strings.Contains(runbook, forbidden) || strings.Contains(changelog, forbidden) {
			t.Fatalf("historical dirty run is still presented as fresh evidence: %q", forbidden)
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

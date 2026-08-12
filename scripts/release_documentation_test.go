package scripts

import (
	"os"
	"strings"
	"testing"
)

func TestReleaseDocumentationNamesEveryCompatibilityChange(t *testing.T) {
	text := readDocumentationFile(t, "../docs/migration-v0.9.1-to-v0.9.2.md")
	for _, phrase := range []string{
		"v0.9.2",
		"strict production configuration",
		"strict migration",
		"`framework.strict`",
		"`framework.response_mode`",
		"`IgniteE`",
		"`Serve`",
		"`CasbinFairing`",
		"`CasbinEnforcer`",
		"16 KiB",
		"`websocket.max_connections`",
		"compatibility defaults",
		"forced security changes",
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

func TestReleaseDocumentationCoversV092RuntimeContracts(t *testing.T) {
	tests := []struct {
		path    string
		phrases []string
	}{
		{
			path: "../docs/production.md",
			phrases: []string{
				"`framework.strict`",
				"`framework.response_mode`",
				"`IgniteE`",
				"`Serve`",
				"`ErrAlreadyServing`",
				"`ErrGinRuntimeConflict`",
				"create a new Bear instance",
				"16 KiB",
				"`websocket.max_connections`",
				"`CasbinEnforcer`",
			},
		},
		{
			path: "../docs/compatibility.md",
			phrases: []string{
				"`framework.strict: false`",
				"`framework.response_mode: raw`",
				"forced security changes",
				"`IgniteE`",
				"`Serve`",
				"16 KiB",
				"Casbin",
				"WebSocket",
			},
		},
	}

	for _, tt := range tests {
		text := readDocumentationFile(t, tt.path)
		for _, phrase := range tt.phrases {
			if !strings.Contains(text, phrase) {
				t.Errorf("%s missing runtime contract %q", tt.path, phrase)
			}
		}
	}
}

func TestReleaseVersionReferencesUseCurrentV09Release(t *testing.T) {
	paths := []string{
		"../README.md",
		"../CHANGELOG.md",
		"../SECURITY.md",
		"../docs/production.md",
		"../docs/runbook.md",
	}
	for _, path := range paths {
		text := readDocumentationFile(t, path)
		if strings.Contains(text, "v0.10.0-rc.1") {
			t.Errorf("%s still names stale unpublished candidate v0.10.0-rc.1", path)
		}
		if !strings.Contains(text, "v0.9.3") {
			t.Errorf("%s does not name current release v0.9.3", path)
		}
	}

	for _, path := range []string{
		"../examples/migration/main.go",
		"../internal/scaffold/scaffold_test.go",
		"release_check_test.go",
		"releasee2e/release_e2e_test.go",
	} {
		text := readDocumentationFile(t, path)
		if strings.Contains(text, "v0.10.0-rc.1") {
			t.Errorf("%s still names stale unpublished candidate v0.10.0-rc.1", path)
		}
	}

	cli := readDocumentationFile(t, "../internal/cli/new.go")
	if strings.Contains(cli, "v0.9.2") || !strings.Contains(cli, "--framework-version") {
		t.Fatal("development CLI must require an explicit framework version without hard-coding a candidate")
	}
}

func TestMigrationMetricsGuidanceMatchesConfigurationAndCompatibilityBehavior(t *testing.T) {
	text := readDocumentationFile(t, "../docs/migration-v0.9.1-to-v0.9.2.md")
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
	text := strings.Join(strings.Fields(readDocumentationFile(t, "../docs/migration-v0.9.1-to-v0.9.2.md")), " ")
	for _, phrase := range []string{
		"`AuthConfig.PublicPaths`",
		"`WebSocketConfig.AllowedOrigins`",
		"new in v0.9.2",
		"v0.9.1 did not expose these fields",
		"early unpublished candidate",
		"`SetPublicPaths` and `GetPublicPaths`",
		"`SetAllowedOrigins` and `GetAllowedOrigins`",
		"pointer identity",
		"v0.9 struct comparability",
	} {
		if !strings.Contains(text, phrase) {
			t.Fatalf("migration collection guidance missing %q", phrase)
		}
	}
	if strings.Contains(text, "Source assignments must be migrated") {
		t.Fatal("migration guide presents an unpublished candidate field shape as a v0.9.1 source migration")
	}
}

func TestProductionDocumentationSeparatesIgniteAndServeErrorStages(t *testing.T) {
	text := strings.Join(strings.Fields(readDocumentationFile(t, "../docs/production.md")), " ")
	for _, phrase := range []string{
		"`IgniteE` returns configuration and Gin runtime construction errors",
		"Strict dependency, build, and lifecycle errors are returned by `ApplyAll` or `Serve`",
	} {
		if !strings.Contains(text, phrase) {
			t.Fatalf("production guide missing startup error-stage contract %q", phrase)
		}
	}
	if strings.Contains(text, "`IgniteE` returns configuration, dependency, and runtime conflicts") {
		t.Fatal("production guide overstates IgniteE dependency validation")
	}
}

func TestV09DevelopmentBranchPolicyHasNoV010Residue(t *testing.T) {
	for _, path := range []string{"verify-rc.sh", "release_check_test.go", "../docs/runbook.md"} {
		text := readDocumentationFile(t, path)
		if !strings.Contains(text, "codex/v09x-framework-hardening") {
			t.Errorf("%s does not allow the current v0.9.x development branch", path)
		}
		if strings.Contains(text, "codex/production-framework-v010") {
			t.Errorf("%s still names the superseded v010 candidate branch", path)
		}
	}
}

func TestReadmeUsesTestedExamplesAndCanonicalCLInstallPath(t *testing.T) {
	text := readDocumentationFile(t, "../README.md")
	for _, phrase := range []string{
		"examples/basic/main.go",
		"examples/auth/main.go",
		"examples/migration/main.go",
		"go install github.com/duiniwukenaihe/gin-bear/cmd/bear@v0.9.3",
		"go test ./...",
	} {
		if !strings.Contains(text, phrase) {
			t.Fatalf("README missing %q", phrase)
		}
	}
}

func TestReleaseDocumentationNamesPublishedVersion(t *testing.T) {
	readme := readDocumentationFile(t, "../README.md")
	security := readDocumentationFile(t, "../SECURITY.md")
	normalizedReadme := strings.ToLower(strings.Join(strings.Fields(readme), " "))
	for _, phrase := range []string{
		"go install github.com/duiniwukenaihe/gin-bear/cmd/bear@v0.9.3",
		"current release",
	} {
		if !strings.Contains(normalizedReadme, strings.ToLower(phrase)) {
			t.Fatalf("README missing publication-state guidance %q", phrase)
		}
	}
	for _, phrase := range []string{"v0.9.3", "current supported release"} {
		if !strings.Contains(strings.ToLower(security), strings.ToLower(phrase)) {
			t.Fatalf("SECURITY.md missing publication-state guidance %q", phrase)
		}
	}
	for path, text := range map[string]string{"README.md": normalizedReadme, "SECURITY.md": strings.ToLower(security)} {
		for _, stale := range []string{"upcoming", "unreleased candidate", "not been pushed, tagged, or published"} {
			if strings.Contains(text, stale) {
				t.Fatalf("%s still describes v0.9.2 as unpublished: %q", path, stale)
			}
		}
	}
}

func TestChangelogSeparatesFutureWorkFromPublishedRelease(t *testing.T) {
	changelog := readDocumentationFile(t, "../CHANGELOG.md")
	for _, heading := range []string{"## [Unreleased]", "## [v0.9.3] - 2026-08-12", "## [v0.9.2] - 2026-08-12"} {
		if !strings.Contains(changelog, heading) {
			t.Fatalf("CHANGELOG.md missing heading %q", heading)
		}
	}
	if strings.Index(changelog, "## [v0.9.3]") > strings.Index(changelog, "## [v0.9.2]") {
		t.Fatal("CHANGELOG.md release headings are not newest-first")
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

func TestReleaseUsesGoModuleAndGitHubSourceAssetsOnly(t *testing.T) {
	if _, err := os.Stat("../.goreleaser.yml"); !os.IsNotExist(err) {
		t.Fatalf("source-only framework release must not retain GoReleaser configuration: %v", err)
	}
	workflow := strings.ToLower(readDocumentationFile(t, "../.github/workflows/release.yml"))
	for _, unwanted := range []string{"goreleaser", "archive", "checksum", "go build", "setup-go"} {
		if strings.Contains(workflow, unwanted) {
			t.Fatalf("source-only release workflow must not contain %q", unwanted)
		}
	}
}

func TestReleaseWorkflowIsTagScopedAndPublishesImmutableRelease(t *testing.T) {
	workflow := readDocumentationFile(t, "../.github/workflows/release.yml")
	for _, phrase := range []string{
		"tags:",
		"- \"v*\"",
		"contents: read",
		"contents: write",
		`gh release create "$GITHUB_REF_NAME"`,
		`--repo "$GITHUB_REPOSITORY"`,
		"--verify-tag",
		"--generate-notes",
		`--title "$GITHUB_REF_NAME"`,
		`gh release view "$GITHUB_REF_NAME"`,
		`https://proxy.golang.org/github.com/${GITHUB_REPOSITORY,,}/@v/${GITHUB_REF_NAME}.info`,
	} {
		if !strings.Contains(workflow, phrase) {
			t.Fatalf("release workflow missing %q", phrase)
		}
	}
	for _, unwanted := range []string{
		"docker", "container", "registry", "attestations: write", "id-token: write",
		"make verify-rc", "staticcheck", "govulncheck", "apidiff", "upload-artifact", "rc-verification",
		"checkout@", "setup-go@", "goreleaser",
		"windows-latest",
	} {
		if strings.Contains(strings.ToLower(workflow), unwanted) {
			t.Fatalf("release workflow must remain CLI-only and permissions-scoped; found %q", unwanted)
		}
	}
	if strings.Contains(workflow, "API_BASELINE_REBUILD") {
		t.Fatal("release workflow must not require API baseline reconstruction")
	}
}

func TestRunbookUsesPinnedVerificationAndSourceOnlyRelease(t *testing.T) {
	runbook := readDocumentationFile(t, "../docs/runbook.md")
	for _, phrase := range []string{
		"GOSUMDB=sum.golang.org GOTOOLCHAIN=go1.25.12 make verify",
		"SHUFFLE_SEED=20260711 STATICCHECK_BIN=/opt/gin-bear/bin/staticcheck STATICCHECK_EXPECTED_SHA256=<trusted-staticcheck-sha256> GOVULNCHECK_BIN=/opt/gin-bear/bin/govulncheck GOVULNCHECK_EXPECTED_SHA256=<trusted-govulncheck-sha256> GOVULNCHECK_DB=file:///opt/gin-bear/vulndb GOVULNCHECK_DB_MANIFEST=/opt/gin-bear/vulndb.manifest.sha256 GOVULNCHECK_DB_MANIFEST_EXPECTED_SHA256=<trusted-manifest-sha256> APIDIFF_BIN=/opt/gin-bear/bin/apidiff APIDIFF_EXPECTED_SHA256=84b7e058a4df23bc0e21d3eae07dedc0b93cee85b40ee8c65701944eed5f742f make verify-rc",
		"GitHub-generated source archives",
		"go install github.com/duiniwukenaihe/gin-bear/cmd/bear@v0.9.3",
	} {
		if !strings.Contains(runbook, phrase) {
			t.Fatalf("runbook missing %q", phrase)
		}
	}
}

func TestReleaseResultIsAuditableAndPublished(t *testing.T) {
	runbook := readDocumentationFile(t, "../docs/runbook.md")
	normalizedRunbook := strings.Join(strings.Fields(runbook), " ")
	for _, phrase := range []string{
		"v0.9.2",
		"75.8%",
		"critical coverage handler 82.9%",
		"critical coverage binding 88.5%",
		"critical coverage lifecycle 84.9%",
		"scripts/check-api-compat.sh",
		"SHUFFLE_SEED=20260711",
		"BEAR_RELEASE_E2E=1 go test ./scripts/releasee2e",
		"formal release gate",
	} {
		if !strings.Contains(normalizedRunbook, phrase) {
			t.Fatalf("runbook missing RC audit evidence %q", phrase)
		}
	}

	changelog := readDocumentationFile(t, "../CHANGELOG.md")
	normalizedChangelog := strings.ToLower(strings.Join(strings.Fields(changelog), " "))
	for _, phrase := range []string{"v0.9.2", "2026-08-12", "formal release gate"} {
		if !strings.Contains(normalizedChangelog, phrase) {
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

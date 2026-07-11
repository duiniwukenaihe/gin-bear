package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
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
		"go mod tidy -diff",
		"scripts/check-api-compat.sh",
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
		"go mod tidy -diff",
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

func TestReleaseCheckOwnsOnlyImplicitCoverageProfile(t *testing.T) {
	for _, test := range []struct {
		name     string
		explicit bool
	}{
		{name: "implicit profile is removed"},
		{name: "explicit profile is retained", explicit: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository, state := fakeReleaseRepository(t)
			before := readTestFile(t, filepath.Join(repository, "go.mod"))
			command := exec.Command("./scripts/release-check.sh")
			command.Dir = repository
			command.Env = append(os.Environ(),
				"PATH="+filepath.Join(repository, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"),
				"RELEASE_TEST_STATE="+state,
				"COVERAGE_MINIMUM=1",
				"CRITICAL_COVERAGE_MINIMUM=1",
			)
			explicitProfile := filepath.Join(repository, "caller-coverage.out")
			if test.explicit {
				command.Env = append(command.Env, "COVERAGE_PROFILE="+explicitProfile)
			}
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("release check failed: %v\n%s", err, output)
			}
			profile := strings.TrimSpace(readTestFile(t, filepath.Join(state, "coverage-profile")))
			_, err := os.Stat(profile)
			if test.explicit && err != nil {
				t.Fatalf("caller-owned coverage profile was removed: %v", err)
			}
			if !test.explicit && !os.IsNotExist(err) {
				t.Fatalf("implicit coverage profile was not removed: %v", err)
			}
			if after := readTestFile(t, filepath.Join(repository, "go.mod")); after != before {
				t.Fatalf("release check polluted go.mod:\nbefore: %q\nafter: %q", before, after)
			}
			goCalls := readTestFile(t, filepath.Join(state, "go-calls"))
			if !strings.Contains(goCalls, "mod tidy -diff") {
				t.Fatalf("release check did not use non-mutating tidy check:\n%s", goCalls)
			}
		})
	}
}

func TestReleaseCheckRemovesOwnedProfileWhenBuildTempCreationFails(t *testing.T) {
	repository, state := fakeReleaseRepository(t)
	fakeMktemp := `#!/usr/bin/env bash
set -euo pipefail
count_file="${RELEASE_TEST_STATE}/mktemp-count"
count=0
if [[ -f "${count_file}" ]]; then count="$(cat "${count_file}")"; fi
count=$((count + 1))
printf '%s\n' "${count}" > "${count_file}"
if [[ "${count}" == "1" ]]; then
	profile="${RELEASE_TEST_STATE}/owned-coverage.out"
	: > "${profile}"
	printf '%s\n' "${profile}" > "${RELEASE_TEST_STATE}/owned-profile-path"
	printf '%s\n' "${profile}"
	exit 0
fi
exit 73
`
	if err := os.WriteFile(filepath.Join(repository, "bin", "mktemp"), []byte(fakeMktemp), 0755); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("./scripts/release-check.sh")
	command.Dir = repository
	command.Env = append(os.Environ(),
		"PATH="+filepath.Join(repository, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"),
		"RELEASE_TEST_STATE="+state,
	)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("release check unexpectedly passed second mktemp failure:\n%s", output)
	}
	profile := strings.TrimSpace(readTestFile(t, filepath.Join(state, "owned-profile-path")))
	if _, statErr := os.Stat(profile); !os.IsNotExist(statErr) {
		t.Fatalf("release-owned profile survived BUILD_DIR mktemp failure: %v\n%s", statErr, output)
	}
}

func TestRCGateAndReleaseWorkflowAreAuditable(t *testing.T) {
	script := readTestFile(t, "verify-rc.sh")
	for _, want := range []string{
		"go clean -testcache",
		"go test ./... -count=1",
		"-shuffle=\"${shuffle_seed}\" -count=20",
		"go test -race ./... -count=3",
		"go vet ./...",
		"staticcheck@v0.7.0",
		"govulncheck@v1.6.0",
		"scripts/release-check.sh",
		"git diff --check",
		"git status --porcelain=v1 --untracked-files=all",
		"exit_code",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("verify-rc.sh missing %q:\n%s", want, script)
		}
	}
	makefile := readTestFile(t, "../Makefile")
	if !strings.Contains(makefile, "verify-rc:") || !strings.Contains(makefile, "scripts/verify-rc.sh") {
		t.Fatalf("Makefile does not expose verify-rc:\n%s", makefile)
	}
	if strings.Contains(makefile, "verify:\n\tscripts/verify-rc.sh") {
		t.Fatalf("ordinary verify unexpectedly runs the full RC gate:\n%s", makefile)
	}
	release := readTestFile(t, "../.github/workflows/release.yml")
	for _, want := range []string{"run: make verify-rc", "actions/upload-artifact", "rc-verification"} {
		if !strings.Contains(release, want) {
			t.Fatalf("release workflow missing %q:\n%s", want, release)
		}
	}
}

func TestVerifyRCRejectsDirtyRepositoryBeforeVersionOrTestCommands(t *testing.T) {
	repository, artifact, state := fakeRCRepository(t)
	output, err := runFakeRC(repository, artifact, state, "RC_TEST_DIRTY=1")
	if err == nil {
		t.Fatalf("verify-rc.sh accepted a dirty repository:\n%s", output)
	}
	if _, statErr := os.Stat(filepath.Join(state, "go-calls")); !os.IsNotExist(statErr) {
		t.Fatalf("verify-rc.sh invoked go before rejecting dirty status: %v\n%s", statErr, output)
	}
}

func TestVerifyRCRecordsTreeAndChecksCompleteCandidateRange(t *testing.T) {
	repository, artifact, state := fakeRCRepository(t)
	output, err := runFakeRC(repository, artifact, state)
	if err != nil {
		t.Fatalf("verify-rc.sh fixture failed: %v\n%s", err, output)
	}
	metadata := readTestFile(t, filepath.Join(artifact, "metadata.txt"))
	for _, want := range []string{"commit=fixture-commit", "tree=fixture-tree", "base_ref=main", "base_commit=fixture-merge-base"} {
		if !strings.Contains(metadata, want) {
			t.Fatalf("metadata missing %q:\n%s", want, metadata)
		}
	}
	gitCalls := readTestFile(t, filepath.Join(state, "git-calls"))
	for _, want := range []string{"diff --check fixture-merge-base..HEAD", "show --check HEAD"} {
		if !strings.Contains(gitCalls, want) {
			t.Fatalf("candidate diff audit missing %q:\n%s", want, gitCalls)
		}
	}
}

func TestVerifyRCRejectsMissingBaseRefClearly(t *testing.T) {
	repository, artifact, state := fakeRCRepository(t)
	output, err := runFakeRC(repository, artifact, state, "RC_TEST_BASE_MISSING=1")
	if err == nil {
		t.Fatalf("verify-rc.sh accepted missing base ref:\n%s", output)
	}
	if !strings.Contains(string(output), "RC base ref does not exist: main") {
		t.Fatalf("missing-base failure is not actionable:\n%s", output)
	}
}

func TestVerifyRCHygieneRejectsUnexpectedLocalBranch(t *testing.T) {
	repository, artifact, state := fakeRCRepository(t)
	branches := "main,codex/production-baseline,codex/production-framework-v010,codex/unreviewed"
	output, err := runFakeRC(repository, artifact, state, "RC_TEST_LOCAL_BRANCHES="+branches)
	if err == nil {
		t.Fatalf("verify-rc.sh accepted unexpected local branch:\n%s", output)
	}
	if !strings.Contains(string(output), "unexpected local branch: codex/unreviewed") {
		t.Fatalf("local-branch failure is not actionable:\n%s", output)
	}
}

func TestVerifyRCHygieneRejectsForbiddenFilesAtAnyDepth(t *testing.T) {
	repository, artifact, state := fakeRCRepository(t)
	forbidden := filepath.Join(repository, "one", "two", "three", "four", "five", "Dockerfile")
	if err := os.MkdirAll(filepath.Dir(forbidden), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(forbidden, []byte("FROM scratch\n"), 0644); err != nil {
		t.Fatal(err)
	}
	output, err := runFakeRC(repository, artifact, state)
	if err == nil {
		t.Fatalf("verify-rc.sh accepted deeply nested Dockerfile:\n%s", output)
	}
	if !strings.Contains(string(output), "Dockerfile") {
		t.Fatalf("forbidden-file failure is not actionable:\n%s", output)
	}
}

func TestAPICompatibilityGateUsesCommittedV091ModuleManifest(t *testing.T) {
	script := readTestFile(t, "check-api-compat.sh")
	for _, want := range []string{
		"golang.org/x/exp/cmd/apidiff@",
		"-m",
		"api/v0.9.1.txt",
		"github.com/duiniwukenaihe/gin-bear",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("API compatibility gate missing %q:\n%s", want, script)
		}
	}
	for _, forbidden := range []string{"git show", "git tag", "git fetch"} {
		if strings.Contains(script, forbidden) {
			t.Fatalf("API compatibility gate depends on repository history via %q:\n%s", forbidden, script)
		}
	}
	manifest := readTestFile(t, "api/v0.9.1.txt")
	for _, packagePath := range []string{
		"github.com/duiniwukenaihe/gin-bear/pkg/bear",
		"github.com/duiniwukenaihe/gin-bear/pkg/bear/gen",
	} {
		if !strings.Contains(manifest, packagePath) {
			t.Fatalf("v0.9.1 API manifest missing public package %q", packagePath)
		}
	}
}

func TestAPICompatibilityGateAcceptsAdditionsAndRejectsIncompatibilities(t *testing.T) {
	for _, test := range []struct {
		name       string
		output     string
		wantFailed bool
	}{
		{name: "additions are compatible"},
		{name: "removal is incompatible", output: "- ./pkg/bear.LegacyExport: removed\n", wantFailed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			bin := filepath.Join(directory, "bin")
			if err := os.MkdirAll(filepath.Join(directory, "api"), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(bin, 0755); err != nil {
				t.Fatal(err)
			}
			script, err := os.ReadFile("check-api-compat.sh")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(directory, "check-api-compat.sh"), script, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(directory, "api", "v0.9.1.txt"), []byte("fixture"), 0644); err != nil {
				t.Fatal(err)
			}
			fakeGo := "#!/usr/bin/env bash\nprintf '%s' \"${FAKE_APIDIFF_OUTPUT:-}\"\n"
			if err := os.WriteFile(filepath.Join(bin, "go"), []byte(fakeGo), 0755); err != nil {
				t.Fatal(err)
			}
			command := exec.Command("./check-api-compat.sh")
			command.Dir = directory
			command.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "FAKE_APIDIFF_OUTPUT="+test.output)
			output, err := command.CombinedOutput()
			if test.wantFailed && err == nil {
				t.Fatalf("API gate accepted incompatible output:\n%s", output)
			}
			if !test.wantFailed && err != nil {
				t.Fatalf("API gate rejected compatible additions: %v\n%s", err, output)
			}
		})
	}
}

func TestGeneratedReleaseE2EUsesPublicCLIAndPreservesGeneratedApp(t *testing.T) {
	source := readTestFile(t, "releasee2e/release_e2e_test.go")
	for _, forbidden := range []string{
		`"github.com/duiniwukenaihe/gin-bear/internal/cli"`,
		`"github.com/duiniwukenaihe/gin-bear/internal/scaffold"`,
		`writeFile(t, filepath.Join(directory, "internal", "app", "app.go")`,
	} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("release E2E bypasses the user-facing scaffold through %q", forbidden)
		}
	}
	for _, want := range []string{
		`buildFixture(t, repository, "./cmd/bear")`,
		`"new", "generated-release-check"`,
		`"gen", "api", "invoice"`,
		`internal", "app", "routes.go"`,
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("release E2E missing public CLI evidence %q:\n%s", want, source)
		}
	}
	stub := readTestFile(t, "releasee2e/release_e2e_windows_test.go")
	for _, want := range []string{"//go:build windows", "t.Skip", "Unix"} {
		if !strings.Contains(stub, want) {
			t.Fatalf("Windows release E2E stub missing %q:\n%s", want, stub)
		}
	}
	ci := readTestFile(t, "../.github/workflows/ci.yml")
	if !strings.Contains(ci, "windows-latest") {
		t.Fatalf("CI does not prove the Windows package set compiles and tests:\n%s", ci)
	}
}

func fakeReleaseRepository(t *testing.T) (string, string) {
	t.Helper()
	repository := t.TempDir()
	state := filepath.Join(repository, "state")
	for _, directory := range []string{filepath.Join(repository, "scripts"), filepath.Join(repository, "bin"), state} {
		if err := os.MkdirAll(directory, 0755); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"release-check.sh", "check-coverage.sh", "critical-coverage-files.txt"} {
		contents, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0644)
		if strings.HasSuffix(name, ".sh") {
			mode = 0755
		}
		if err := os.WriteFile(filepath.Join(repository, "scripts", name), contents, mode); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(repository, "scripts", "check-coverage.sh"), []byte("#!/usr/bin/env bash\nset -euo pipefail\ntest -s \"$1\"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, "scripts", "check-api-compat.sh"), []byte("#!/usr/bin/env bash\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, "go.mod"), []byte("module example.com/release-test\n\ngo 1.25.0\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, "go.sum"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	fakeGo := `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "${RELEASE_TEST_STATE}/go-calls"
if [[ "$1 $2" == "list -m" ]]; then
	printf '%s\n' example.com/release-test
	exit 0
fi
if [[ "$1 $2" == "mod tidy" && "${3:-}" != "-diff" ]]; then
	printf '%s\n' polluted >> go.mod
fi
for arg in "$@"; do
	case "$arg" in
	-coverprofile=*)
		profile="${arg#-coverprofile=}"
		mkdir -p "$(dirname "$profile")"
		printf 'mode: set\nexample.com/release-test/sample.go:1.1,1.2 1 1\n' > "$profile"
		printf '%s\n' "$profile" > "${RELEASE_TEST_STATE}/coverage-profile"
		;;
	esac
done
exit 0
`
	fakeGit := `#!/usr/bin/env bash
set -euo pipefail
if [[ "${1:-}" == "rev-parse" ]]; then printf '%s\n' deadbee; fi
exit 0
`
	for name, contents := range map[string]string{"go": fakeGo, "git": fakeGit} {
		if err := os.WriteFile(filepath.Join(repository, "bin", name), []byte(contents), 0755); err != nil {
			t.Fatal(err)
		}
	}
	return repository, state
}

func fakeRCRepository(t *testing.T) (string, string, string) {
	t.Helper()
	root := t.TempDir()
	repository := filepath.Join(root, "repository")
	artifact := filepath.Join(root, "artifact")
	state := filepath.Join(root, "state")
	for _, directory := range []string{filepath.Join(repository, "scripts"), filepath.Join(repository, "bin"), artifact, state} {
		if err := os.MkdirAll(directory, 0755); err != nil {
			t.Fatal(err)
		}
	}
	verifyScript, err := os.ReadFile("verify-rc.sh")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, "scripts", "verify-rc.sh"), verifyScript, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, "scripts", "release-check.sh"), []byte("#!/usr/bin/env bash\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	fakeGo := `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "${RC_TEST_STATE}/go-calls"
if [[ "${1:-}" == "version" ]]; then printf '%s\n' 'go version go1.25.12 fixture'; fi
if [[ "${1:-} ${2:-} ${3:-}" == "run honnef.co/go/tools/cmd/staticcheck@v0.7.0 -version" ]]; then printf '%s\n' 'staticcheck fixture'; fi
if [[ "${1:-} ${2:-} ${3:-}" == "run golang.org/x/vuln/cmd/govulncheck@v1.6.0 -version" ]]; then printf '%s\n' 'govulncheck fixture'; fi
exit 0
`
	fakeGit := `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "${RC_TEST_STATE}/git-calls"
case "${1:-}" in
	status)
		if [[ "${RC_TEST_DIRTY:-0}" == "1" ]]; then printf '%s\n' ' M tracked.go'; fi
		;;
	rev-parse)
		if [[ "${2:-}" == "HEAD" ]]; then printf '%s\n' fixture-commit
		elif [[ "${2:-}" == 'HEAD^{tree}' ]]; then printf '%s\n' fixture-tree
		elif [[ "${2:-}" == "--verify" ]]; then
			if [[ "${RC_TEST_BASE_MISSING:-0}" == "1" ]]; then exit 1; fi
			printf '%s\n' fixture-base
		fi
		;;
	merge-base) printf '%s\n' fixture-merge-base ;;
	for-each-ref)
		branches="${RC_TEST_LOCAL_BRANCHES:-main,codex/production-baseline,codex/production-framework-v010}"
		printf '%s\n' "${branches}" | tr ',' '\n'
		;;
	branch)
		branches="${RC_TEST_LOCAL_BRANCHES:-main,codex/production-baseline,codex/production-framework-v010}"
		printf '%s\n' "${branches}" | tr ',' '\n'
		;;
	ls-remote)
		printf '%s\n' 'fixture refs/heads/main'
		printf '%s\n' 'fixture refs/heads/codex/production-baseline'
		;;
esac
exit 0
`
	for name, contents := range map[string]string{"go": fakeGo, "git": fakeGit} {
		if err := os.WriteFile(filepath.Join(repository, "bin", name), []byte(contents), 0755); err != nil {
			t.Fatal(err)
		}
	}
	return repository, artifact, state
}

func runFakeRC(repository, artifact, state string, extraEnvironment ...string) ([]byte, error) {
	command := exec.Command("./scripts/verify-rc.sh")
	command.Dir = repository
	command.Env = append(os.Environ(),
		"PATH="+filepath.Join(repository, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"),
		"RC_ARTIFACT_DIR="+artifact,
		"RC_TEST_STATE="+state,
		"RC_BASE_REF=main",
		"SHUFFLE_SEED=20260711",
	)
	command.Env = append(command.Env, extraEnvironment...)
	return command.CombinedOutput()
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
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

package scripts

import (
	"crypto/sha256"
	"fmt"
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
		`CGO_ENABLED=0 GOBIN="${RUNNER_TEMP}/bin" go install -trimpath -ldflags=-buildid= honnef.co/go/tools/cmd/staticcheck@v0.7.0`,
		`CGO_ENABLED=0 GOBIN="${RUNNER_TEMP}/bin" go install -trimpath -ldflags=-buildid= golang.org/x/vuln/cmd/govulncheck@v1.6.0`,
		`CGO_ENABLED=0 GOBIN="${RUNNER_TEMP}/bin" go install -trimpath -ldflags=-buildid= golang.org/x/exp/cmd/apidiff@v0.0.0-20260709172345-9ea1abe57597`,
		"STATICCHECK_BIN: ${{ runner.temp }}/bin/staticcheck",
		"STATICCHECK_EXPECTED_SHA256: 968c4cdeff3a18eef976ecdbcd83dbea35ca3c12c58b87c9f4684e1ea6adfc75",
		"GOVULNCHECK_BIN: ${{ runner.temp }}/bin/govulncheck",
		"GOVULNCHECK_EXPECTED_SHA256: 15ad0c7081d061d06f83a39b1783318d90659f3404d83a75bcdda51eda3ef75f",
		"APIDIFF_BIN: ${{ runner.temp }}/bin/apidiff",
		"APIDIFF_EXPECTED_SHA256: 84b7e058a4df23bc0e21d3eae07dedc0b93cee85b40ee8c65701944eed5f742f",
		"RC_ALLOW_NETWORK: \"1\"",
		"RELEASE_CHECK_METADATA: ${{ runner.temp }}/release-check-metadata.txt",
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

func TestReleaseCheckPersistsExplicitNetworkEvidence(t *testing.T) {
	for _, test := range []struct {
		name string
		flag string
		mode string
	}{
		{name: "offline", flag: "0", mode: "offline"},
		{name: "online opt in", flag: "1", mode: "online-opt-in"},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository, state := fakeReleaseRepository(t)
			metadata := filepath.Join(state, "release-check-metadata.txt")
			command := exec.Command("./scripts/release-check.sh")
			command.Dir = repository
			command.Env = releaseTestEnvironment(
				"PATH="+filepath.Join(repository, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"),
				"RELEASE_TEST_STATE="+state,
				"RC_ALLOW_NETWORK="+test.flag,
				"RELEASE_CHECK_METADATA="+metadata,
				"GOVULNCHECK_DB=file://"+filepath.Join(repository, "vulndb"),
			)
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("release check failed: %v\n%s", err, output)
			}
			for _, evidence := range []string{string(output), readTestFile(t, metadata)} {
				for _, want := range []string{
					"release_check_network=" + test.mode,
					"release_check_network_opt_in=" + test.flag,
				} {
					if !strings.Contains(evidence, want) {
						t.Fatalf("release-check evidence missing %q:\n%s", want, evidence)
					}
				}
			}
			if !strings.Contains(string(output), "release_check_metadata="+metadata) {
				t.Fatalf("release-check stdout does not identify persisted metadata:\n%s", output)
			}
		})
	}
}

func TestReleaseCheckRejectsUnverifiedControlledStaticcheck(t *testing.T) {
	repository, state := fakeReleaseRepository(t)
	staticcheck := systemTrue(t)
	command := exec.Command("./scripts/release-check.sh")
	command.Dir = repository
	command.Env = releaseTestEnvironment(
		"PATH="+filepath.Join(repository, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"),
		"RELEASE_TEST_STATE="+state,
		"RC_ALLOW_NETWORK=0",
		"STATICCHECK_BIN="+staticcheck,
		"STATICCHECK_EXPECTED_SHA256="+fileSHA256(t, staticcheck),
		"GOVULNCHECK_BIN="+filepath.Join(repository, "bin", "govulncheck"),
		"GOVULNCHECK_EXPECTED_SHA256="+fileSHA256(t, filepath.Join(repository, "bin", "govulncheck")),
		"GOVULNCHECK_DB=file://"+filepath.Join(repository, "vulndb"),
	)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("release-check.sh accepted %s as staticcheck:\n%s", staticcheck, output)
	}
	if !strings.Contains(string(output), "STATICCHECK_BIN") {
		t.Fatalf("unverified staticcheck failure is not actionable:\n%s", output)
	}
}

func TestReleaseCheckRejectsControlledToolDigestMismatch(t *testing.T) {
	repository, state := fakeReleaseRepository(t)
	command := exec.Command("./scripts/release-check.sh")
	command.Dir = repository
	command.Env = releaseTestEnvironment(
		"PATH="+filepath.Join(repository, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"),
		"RELEASE_TEST_STATE="+state,
		"RC_ALLOW_NETWORK=0",
		"STATICCHECK_BIN="+filepath.Join(repository, "bin", "staticcheck"),
		"STATICCHECK_EXPECTED_SHA256="+strings.Repeat("0", 64),
		"GOVULNCHECK_BIN="+filepath.Join(repository, "bin", "govulncheck"),
		"GOVULNCHECK_EXPECTED_SHA256="+fileSHA256(t, filepath.Join(repository, "bin", "govulncheck")),
		"GOVULNCHECK_DB=file://"+filepath.Join(repository, "vulndb"),
	)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("release-check.sh accepted a staticcheck digest mismatch:\n%s", output)
	}
	if !strings.Contains(string(output), "STATICCHECK_EXPECTED_SHA256") {
		t.Fatalf("staticcheck digest failure is not actionable:\n%s", output)
	}
}

func TestVerifyRCRejectsUnverifiedControlledGovulncheck(t *testing.T) {
	repository, artifact, state := fakeRCRepository(t)
	govulncheck := systemTrue(t)
	output, err := runFakeRC(repository, artifact, state,
		"GOVULNCHECK_BIN="+govulncheck,
		"GOVULNCHECK_EXPECTED_SHA256="+fileSHA256(t, govulncheck),
		"STATICCHECK_BIN="+filepath.Join(repository, "bin", "staticcheck"),
		"STATICCHECK_EXPECTED_SHA256="+fileSHA256(t, filepath.Join(repository, "bin", "staticcheck")),
	)
	if err == nil {
		t.Fatalf("verify-rc.sh accepted %s as govulncheck:\n%s", govulncheck, output)
	}
	if !strings.Contains(string(output), "GOVULNCHECK_BIN") {
		t.Fatalf("unverified govulncheck failure is not actionable:\n%s", output)
	}
}

func TestReleaseCheckRejectsRemoteDatabaseInOfflineMode(t *testing.T) {
	repository, state := fakeReleaseRepository(t)
	command := exec.Command("./scripts/release-check.sh")
	command.Dir = repository
	command.Env = releaseTestEnvironment(
		"PATH="+filepath.Join(repository, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"),
		"RELEASE_TEST_STATE="+state,
		"RC_ALLOW_NETWORK=0",
		"GOVULNCHECK_DB=https://vuln.go.dev",
	)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("offline release check accepted remote vulnerability database:\n%s", output)
	}
	if !strings.Contains(string(output), "local absolute path or file:// URI") {
		t.Fatalf("remote database failure is not actionable:\n%s", output)
	}
}

func TestVerifyRCRecordsOfflineDatabaseManifestIdentity(t *testing.T) {
	repository, artifact, state := fakeRCRepository(t)
	if output, err := runFakeRC(repository, artifact, state); err != nil {
		t.Fatalf("offline verify-rc.sh fixture failed: %v\n%s", err, output)
	}
	metadata := readTestFile(t, filepath.Join(artifact, "metadata.txt"))
	for _, want := range []string{
		"govulncheck_db_source=local-snapshot",
		"govulncheck_db_path=",
		"govulncheck_db_manifest_path=",
		"govulncheck_db_manifest_expected_sha256=",
		"govulncheck_db_manifest_actual_sha256=",
	} {
		if !strings.Contains(metadata, want) {
			t.Fatalf("offline database metadata missing %q:\n%s", want, metadata)
		}
	}
}

func TestVerifyRCOnlineModeOmitsOfflineDatabaseEvidence(t *testing.T) {
	repository, artifact, state := fakeRCRepository(t)
	output, err := runFakeRC(repository, artifact, state, "RC_ALLOW_NETWORK=1")
	if err != nil {
		t.Fatalf("online verify-rc.sh fixture failed: %v\n%s", err, output)
	}
	metadata := readTestFile(t, filepath.Join(artifact, "metadata.txt"))
	if !strings.Contains(metadata, "network_mode=online-opt-in") {
		t.Fatalf("online metadata missing network mode:\n%s", metadata)
	}
	if strings.Contains(metadata, "govulncheck_db_source=") {
		t.Fatalf("online metadata declared offline database evidence:\n%s", metadata)
	}
}

func TestOfflineDatabaseManifestMustCoverExactFileTree(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(*testing.T, string)
		wantError string
	}{
		{
			name: "unlisted file",
			mutate: func(t *testing.T, database string) {
				path := filepath.Join(database, "ID", "GO-UNLISTED.json")
				if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("{}\n"), 0644); err != nil {
					t.Fatal(err)
				}
			},
			wantError: "must cover exactly",
		},
		{
			name: "nested symlink",
			mutate: func(t *testing.T, database string) {
				link := filepath.Join(database, "index", "alias.json")
				if err := os.Symlink("db.json", link); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			},
			wantError: "must not contain symbolic links",
		},
		{
			name: "duplicate manifest path",
			mutate: func(t *testing.T, _ string) {
				manifest := os.Getenv("GOVULNCHECK_DB_MANIFEST")
				contents, err := os.ReadFile(manifest)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(manifest, append(contents, contents...), 0644); err != nil {
					t.Fatal(err)
				}
				t.Setenv("GOVULNCHECK_DB_MANIFEST_EXPECTED_SHA256", fileSHA256(t, manifest))
			},
			wantError: "duplicate path",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repository, state := fakeReleaseRepository(t)
			database := strings.TrimPrefix(os.Getenv("GOVULNCHECK_DB"), "file://")
			tt.mutate(t, database)
			command := exec.Command("./scripts/release-check.sh")
			command.Dir = repository
			command.Env = releaseTestEnvironment(
				"PATH="+filepath.Join(repository, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"),
				"RELEASE_TEST_STATE="+state,
				"RC_ALLOW_NETWORK=0",
			)
			output, err := command.CombinedOutput()
			if err == nil {
				t.Fatalf("offline release check accepted invalid database tree:\n%s", output)
			}
			if !strings.Contains(string(output), tt.wantError) {
				t.Fatalf("database tree failure missing %q:\n%s", tt.wantError, output)
			}
		})
	}
}

func TestAllCICheckoutAndSetupGoActionsUseReleasePins(t *testing.T) {
	ci := readTestFile(t, "../.github/workflows/ci.yml")
	checkout := "actions/checkout@34e114876b0b11c390a56381ad16ebd13914f8d5 # v4.3.1"
	setupGo := "actions/setup-go@40f1582b2485089dde7abd97c1529aa768e1baff # v5.6.0"
	if count := strings.Count(ci, checkout); count != 3 {
		t.Fatalf("CI checkout pin count=%d, want 3:\n%s", count, ci)
	}
	if count := strings.Count(ci, setupGo); count != 3 {
		t.Fatalf("CI setup-go pin count=%d, want 3:\n%s", count, ci)
	}
	for _, mutable := range []string{"actions/checkout@v", "actions/setup-go@v"} {
		if strings.Contains(ci, mutable) {
			t.Fatalf("CI retains mutable action reference %q", mutable)
		}
	}
}

func TestReleaseExpectedVersionComesFromPushedTag(t *testing.T) {
	workflow := readTestFile(t, "../.github/workflows/release.yml")
	releaseConfig := readTestFile(t, "../.goreleaser.yml")
	if !strings.Contains(workflow, "RC_EXPECTED_VERSION: ${{ github.ref_name }}") {
		t.Fatalf("release workflow does not pass the pushed tag as the expected module version:\n%s", workflow)
	}
	if strings.Contains(workflow, "RC_EXPECTED_VERSION: v0.10.0-rc.1") {
		t.Fatalf("release workflow pins every v* tag to rc.1:\n%s", workflow)
	}
	if !strings.Contains(workflow, `CGO_ENABLED=0 GOBIN="${RUNNER_TEMP}/bin" go install -trimpath -ldflags=-buildid= golang.org/x/exp/cmd/apidiff@`) ||
		!strings.Contains(workflow, "APIDIFF_BIN: ${{ runner.temp }}/bin/apidiff") ||
		!strings.Contains(workflow, "APIDIFF_EXPECTED_SHA256: 84b7e058a4df23bc0e21d3eae07dedc0b93cee85b40ee8c65701944eed5f742f") ||
		!strings.Contains(workflow, "STATICCHECK_EXPECTED_SHA256: 968c4cdeff3a18eef976ecdbcd83dbea35ca3c12c58b87c9f4684e1ea6adfc75") ||
		!strings.Contains(workflow, "GOVULNCHECK_EXPECTED_SHA256: 15ad0c7081d061d06f83a39b1783318d90659f3404d83a75bcdda51eda3ef75f") {
		t.Fatalf("release workflow does not prepare and pass a pinned local apidiff binary:\n%s", workflow)
	}
	if strings.Contains(workflow, "API_COMPAT_ALLOW_NETWORK:") {
		t.Fatalf("release verification gate enables API network fallback:\n%s", workflow)
	}
	if !strings.Contains(releaseConfig, "pkg/bear.Version={{ .Version }}") || !strings.Contains(readTestFile(t, "../internal/cli/new.go"), "bear.Version") {
		t.Fatalf("released CLI does not derive its scaffold default from the GoReleaser-injected version:\n%s", releaseConfig)
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
			command.Env = releaseTestEnvironment(
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

func TestReleaseCheckForcesNonDowngradableCoverageThresholds(t *testing.T) {
	repository, state := fakeReleaseRepository(t)
	command := exec.Command("./scripts/release-check.sh")
	command.Dir = repository
	command.Env = releaseTestEnvironment(
		"PATH="+filepath.Join(repository, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"),
		"RELEASE_TEST_STATE="+state,
		"COVERAGE_MINIMUM=1",
		"CRITICAL_COVERAGE_MINIMUM=1",
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("release check failed: %v\n%s", err, output)
	}
	thresholds := strings.TrimSpace(readTestFile(t, filepath.Join(state, "coverage-thresholds")))
	if thresholds != "70.0 80.0" {
		t.Fatalf("release coverage thresholds=%q, want enforced 70.0 80.0", thresholds)
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
	command.Env = releaseTestEnvironment(
		"PATH="+filepath.Join(repository, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"),
		"RELEASE_TEST_STATE="+state,
		"RELEASE_CHECK_METADATA="+filepath.Join(state, "release-check-metadata.txt"),
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
		"-shuffle=\"${shuffle_seed}\" -count=20 -timeout=\"${shuffle_timeout}\"",
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

func TestVerifyRCDerivesStableShuffleSeedFromCandidate(t *testing.T) {
	repository, artifact, state := fakeRCRepository(t)
	firstArtifact := filepath.Join(artifact, "first")
	secondArtifact := filepath.Join(artifact, "second")
	if output, err := runFakeRC(repository, firstArtifact, state, "RC_TEST_DATE=111"); err != nil {
		t.Fatalf("first verify-rc.sh fixture failed: %v\n%s", err, output)
	}
	if output, err := runFakeRC(repository, secondArtifact, state, "RC_TEST_DATE=222"); err != nil {
		t.Fatalf("second verify-rc.sh fixture failed: %v\n%s", err, output)
	}
	first := metadataValue(t, filepath.Join(firstArtifact, "metadata.txt"), "shuffle_seed")
	second := metadataValue(t, filepath.Join(secondArtifact, "metadata.txt"), "shuffle_seed")
	if first == "" || first != second {
		t.Fatalf("derived shuffle seeds differ for the same candidate: first=%q second=%q", first, second)
	}
}

func TestVerifyRCRecordsConfigurableShuffleTimeout(t *testing.T) {
	repository, artifact, state := fakeRCRepository(t)
	if output, err := runFakeRC(repository, artifact, state, "RC_SHUFFLE_TIMEOUT=75m"); err != nil {
		t.Fatalf("verify-rc.sh fixture failed: %v\n%s", err, output)
	}
	if got := metadataValue(t, filepath.Join(artifact, "metadata.txt"), "shuffle_timeout"); got != "75m" {
		t.Fatalf("shuffle_timeout = %q, want 75m", got)
	}
	if calls := readTestFile(t, filepath.Join(state, "go-calls")); !strings.Contains(calls, "-count=20 -timeout=75m") {
		t.Fatalf("shuffle timeout was not passed to go test:\n%s", calls)
	}
}

func TestVerifyRCDefaultsShuffleTimeout(t *testing.T) {
	repository, artifact, state := fakeRCRepository(t)
	if output, err := runFakeRC(repository, artifact, state); err != nil {
		t.Fatalf("verify-rc.sh fixture failed: %v\n%s", err, output)
	}
	if got := metadataValue(t, filepath.Join(artifact, "metadata.txt"), "shuffle_timeout"); got != "60m" {
		t.Fatalf("shuffle_timeout = %q, want 60m", got)
	}
	if calls := readTestFile(t, filepath.Join(state, "go-calls")); !strings.Contains(calls, "-count=20 -timeout=60m") {
		t.Fatalf("default shuffle timeout was not passed to go test:\n%s", calls)
	}
}

func TestVerifyRCRejectsInvalidShuffleTimeout(t *testing.T) {
	for _, value := range []string{"", "0m", "30", "1.5h", "30m;false"} {
		t.Run(strings.ReplaceAll(value, "/", "_"), func(t *testing.T) {
			repository, artifact, state := fakeRCRepository(t)
			output, err := runFakeRC(repository, artifact, state, "RC_SHUFFLE_TIMEOUT="+value)
			if err == nil {
				t.Fatalf("verify-rc.sh accepted invalid RC_SHUFFLE_TIMEOUT %q:\n%s", value, output)
			}
		})
	}
}

func TestVerifyRCRemoteHygieneIsExplicitOptIn(t *testing.T) {
	repository, artifact, state := fakeRCRepository(t)
	if output, err := runFakeRC(repository, artifact, state); err != nil {
		t.Fatalf("offline verify-rc.sh fixture failed: %v\n%s", err, output)
	}
	if calls := readTestFile(t, filepath.Join(state, "git-calls")); strings.Contains(calls, "ls-remote") {
		t.Fatalf("default RC verification used the network:\n%s", calls)
	}

	remoteArtifact := filepath.Join(filepath.Dir(artifact), "remote-artifact")
	if output, err := runFakeRC(repository, remoteArtifact, state, "RC_REMOTE_HYGIENE=1"); err != nil {
		t.Fatalf("opt-in remote hygiene failed: %v\n%s", err, output)
	}
	if calls := readTestFile(t, filepath.Join(state, "git-calls")); !strings.Contains(calls, "ls-remote --heads origin") {
		t.Fatalf("opt-in remote hygiene did not inspect origin:\n%s", calls)
	}
}

func TestVerifyRCDefaultModeUsesLocalToolsWithoutNetworkCommands(t *testing.T) {
	repository, artifact, state := fakeRCRepository(t)
	if output, err := runFakeRC(repository, artifact, state); err != nil {
		t.Fatalf("offline verify-rc.sh fixture failed: %v\n%s", err, output)
	}
	goCalls := readTestFile(t, filepath.Join(state, "go-calls"))
	if strings.Contains(goCalls, " run ") || strings.Contains(goCalls, "GOPROXY=https://") {
		t.Fatalf("default RC verification attempted a network-capable command:\n%s", goCalls)
	}
	for _, line := range strings.Split(strings.TrimSpace(goCalls), "\n") {
		if !strings.HasPrefix(line, "GOPROXY=off ") {
			t.Fatalf("default RC Go command was not forced offline: %q\n%s", line, goCalls)
		}
	}
	metadata := readTestFile(t, filepath.Join(artifact, "metadata.txt"))
	for _, want := range []string{
		"network_mode=offline",
		"GOPROXY=off",
		"staticcheck_source=controlled-binary",
		"govulncheck_source=controlled-binary",
		"staticcheck_expected_sha256=",
		"staticcheck_actual_sha256=",
		"govulncheck_expected_sha256=",
		"govulncheck_actual_sha256=",
	} {
		if !strings.Contains(metadata, want) {
			t.Fatalf("offline metadata missing %q:\n%s", want, metadata)
		}
	}
	if calls := readTestFile(t, filepath.Join(state, "govulncheck-calls")); !strings.Contains(calls, "-db ") {
		t.Fatalf("offline govulncheck did not use the controlled local database:\n%s", calls)
	}
}

func TestVerifyRCRejectsInvalidRemoteHygieneFlag(t *testing.T) {
	repository, artifact, state := fakeRCRepository(t)
	output, err := runFakeRC(repository, artifact, state, "RC_REMOTE_HYGIENE=banana")
	if err == nil {
		t.Fatalf("verify-rc.sh accepted an invalid remote hygiene flag:\n%s", output)
	}
	if !strings.Contains(string(output), "RC_REMOTE_HYGIENE must be 0 or 1") {
		t.Fatalf("invalid remote hygiene failure is not actionable:\n%s", output)
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

func TestVerifyRCRejectsBaseThatIsNotHEADAncestor(t *testing.T) {
	repository, artifact, state := fakeRCRepository(t)
	output, err := runFakeRC(repository, artifact, state, "RC_TEST_BASE_NOT_ANCESTOR=1")
	if err == nil {
		t.Fatalf("verify-rc.sh accepted a non-ancestor base:\n%s", output)
	}
	if !strings.Contains(string(output), "must be an ancestor of HEAD") {
		t.Fatalf("non-ancestor failure is not actionable:\n%s", output)
	}
}

func TestVerifyRCValidatesAnnotatedReleaseTagAndRecordsSignatureBoundary(t *testing.T) {
	repository, artifact, state := fakeRCRepository(t)
	output, err := runFakeRC(repository, artifact, state,
		"RC_RELEASE_TAG=v0.10.0-rc.1",
		"RC_EXPECTED_VERSION=v0.10.0-rc.1",
		"RC_VERIFY_TAG_SIGNATURE=false",
	)
	if err != nil {
		t.Fatalf("verify-rc.sh fixture failed: %v\n%s", err, output)
	}
	metadata := readTestFile(t, filepath.Join(artifact, "metadata.txt"))
	for _, want := range []string{
		"release_tag=v0.10.0-rc.1",
		"release_tag_type=tag",
		"release_tag_target=fixture-commit",
		"signature_policy=false",
		"tag_signature_verification=explicitly-exempted",
		"coverage_minimum=70.0",
		"critical_coverage_minimum=80.0",
	} {
		if !strings.Contains(metadata, want) {
			t.Fatalf("release metadata missing %q:\n%s", want, metadata)
		}
	}
}

func TestVerifyRCRequiresExplicitSignaturePolicyForReleaseTags(t *testing.T) {
	repository, artifact, state := fakeRCRepository(t)
	output, err := runFakeRC(repository, artifact, state,
		"RC_RELEASE_TAG=v0.10.0-rc.1",
		"RC_EXPECTED_VERSION=v0.10.0-rc.1",
	)
	if err == nil {
		t.Fatalf("verify-rc.sh accepted a release tag without an explicit signature policy:\n%s", output)
	}
	if !strings.Contains(string(output), "RC_VERIFY_TAG_SIGNATURE must be explicitly set to true or false when RC_RELEASE_TAG is set") {
		t.Fatalf("missing signature policy failure is not actionable:\n%s", output)
	}
}

func TestVerifyRCSignatureVerificationRequiresTrustedKeyring(t *testing.T) {
	repository, artifact, state := fakeRCRepository(t)
	output, err := runFakeRC(repository, artifact, state,
		"RC_RELEASE_TAG=v0.10.0-rc.1",
		"RC_EXPECTED_VERSION=v0.10.0-rc.1",
		"RC_VERIFY_TAG_SIGNATURE=true",
	)
	if err == nil {
		t.Fatalf("verify-rc.sh verified a release tag without a trusted keyring:\n%s", output)
	}
	if !strings.Contains(string(output), "RC_TRUSTED_KEYRING") {
		t.Fatalf("missing trusted keyring failure is not actionable:\n%s", output)
	}
}

func TestVerifyRCSignatureVerificationRequiresTrustedStatus(t *testing.T) {
	repository, artifact, state := fakeRCRepository(t)
	keyring := filepath.Join(repository, "trusted-gnupg")
	if err := os.MkdirAll(keyring, 0700); err != nil {
		t.Fatal(err)
	}
	output, err := runFakeRC(repository, artifact, state,
		"RC_RELEASE_TAG=v0.10.0-rc.1",
		"RC_EXPECTED_VERSION=v0.10.0-rc.1",
		"RC_VERIFY_TAG_SIGNATURE=true",
		"RC_TRUSTED_KEYRING="+keyring,
		"RC_TEST_SIGNATURE_UNTRUSTED=1",
	)
	if err == nil {
		t.Fatalf("verify-rc.sh accepted a valid but untrusted tag signature:\n%s", output)
	}
	if !strings.Contains(string(output), "not trusted by RC_TRUSTED_KEYRING") {
		t.Fatalf("untrusted signature failure is not actionable:\n%s", output)
	}
}

func TestVerifyRCRecordsTrustedSignatureVerification(t *testing.T) {
	repository, artifact, state := fakeRCRepository(t)
	keyring := filepath.Join(repository, "trusted-gnupg")
	if err := os.MkdirAll(keyring, 0700); err != nil {
		t.Fatal(err)
	}
	output, err := runFakeRC(repository, artifact, state,
		"RC_RELEASE_TAG=v0.10.0-rc.1",
		"RC_EXPECTED_VERSION=v0.10.0-rc.1",
		"RC_VERIFY_TAG_SIGNATURE=true",
		"RC_TRUSTED_KEYRING="+keyring,
	)
	if err != nil {
		t.Fatalf("trusted signature fixture failed: %v\n%s", err, output)
	}
	metadata := readTestFile(t, filepath.Join(artifact, "metadata.txt"))
	for _, want := range []string{"signature_policy=true", "tag_signature_verification=verified-with-trusted-keyring"} {
		if !strings.Contains(metadata, want) {
			t.Fatalf("trusted signature metadata missing %q:\n%s", want, metadata)
		}
	}
}

func TestVerifyRCRejectsInvalidReleaseTagContracts(t *testing.T) {
	for _, test := range []struct {
		name  string
		env   []string
		match string
	}{
		{name: "lightweight", env: []string{"RC_TEST_TAG_TYPE=commit"}, match: "must be annotated"},
		{name: "wrong target", env: []string{"RC_TEST_TAG_TARGET=other-commit"}, match: "must target HEAD"},
		{name: "version mismatch", env: []string{"RC_EXPECTED_VERSION=v0.10.0"}, match: "does not match release version"},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository, artifact, state := fakeRCRepository(t)
			environment := []string{
				"RC_RELEASE_TAG=v0.10.0-rc.1",
				"RC_EXPECTED_VERSION=v0.10.0-rc.1",
				"RC_VERIFY_TAG_SIGNATURE=false",
			}
			environment = append(environment, test.env...)
			output, err := runFakeRC(repository, artifact, state, environment...)
			if err == nil {
				t.Fatalf("verify-rc.sh accepted invalid tag contract:\n%s", output)
			}
			if !strings.Contains(string(output), test.match) {
				t.Fatalf("tag contract failure missing %q:\n%s", test.match, output)
			}
		})
	}
}

func TestVerifyRCAlwaysParsesSignatureFlag(t *testing.T) {
	for _, value := range []string{"", "banana"} {
		t.Run("invalid-"+value, func(t *testing.T) {
			repository, artifact, state := fakeRCRepository(t)
			output, err := runFakeRC(repository, artifact, state, "RC_VERIFY_TAG_SIGNATURE="+value)
			if err == nil {
				t.Fatalf("verify-rc.sh accepted invalid signature flag %q without a tag:\n%s", value, output)
			}
			if !strings.Contains(string(output), "RC_VERIFY_TAG_SIGNATURE must be true or false") {
				t.Fatalf("invalid signature flag failure is not actionable:\n%s", output)
			}
		})
	}
}

func TestVerifyRCRejectsSignaturePolicyWithoutReleaseTag(t *testing.T) {
	for _, value := range []string{"true", "false"} {
		t.Run(value, func(t *testing.T) {
			repository, artifact, state := fakeRCRepository(t)
			output, err := runFakeRC(repository, artifact, state, "RC_VERIFY_TAG_SIGNATURE="+value)
			if err == nil {
				t.Fatalf("verify-rc.sh accepted signature policy %q without a release tag:\n%s", value, output)
			}
			if !strings.Contains(string(output), "RC_VERIFY_TAG_SIGNATURE requires RC_RELEASE_TAG") {
				t.Fatalf("missing-tag signature failure is not actionable:\n%s", output)
			}
		})
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
		"api/v0.9.1.txt.sha256",
		"sha256",
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

func TestAPICompatibilityBaselineMatchesCommittedSHA256(t *testing.T) {
	baseline, err := os.ReadFile("api/v0.9.1.txt")
	if err != nil {
		t.Fatal(err)
	}
	checksum := strings.Fields(readTestFile(t, "api/v0.9.1.txt.sha256"))
	if len(checksum) != 2 || checksum[1] != "v0.9.1.txt" {
		t.Fatalf("invalid checksum file: %q", checksum)
	}
	want := strings.ToLower(checksum[0])
	got := sha256.Sum256(baseline)
	if actual := fmt.Sprintf("%x", got); actual != want {
		t.Fatalf("baseline SHA256=%s, want %s", actual, want)
	}
}

func TestAPICompatibilityGateOffersModuleCacheRebuildWithoutGitHistory(t *testing.T) {
	script := readTestFile(t, "check-api-compat.sh")
	for _, want := range []string{"API_BASELINE_REBUILD", "go mod download -json", `baseline_version="v0.9.1"`, `"${module}@${baseline_version}"`} {
		if !strings.Contains(script, want) {
			t.Fatalf("API compatibility rebuild missing %q:\n%s", want, script)
		}
	}
	for _, forbidden := range []string{"git clone", "--depth", "git fetch"} {
		if strings.Contains(script, forbidden) {
			t.Fatalf("API compatibility rebuild depends on repository history via %q", forbidden)
		}
	}
}

func TestAPICompatibilityGateRejectsInvalidRebuildFlag(t *testing.T) {
	for _, value := range []string{"", "banana"} {
		t.Run(value, func(t *testing.T) {
			command := exec.Command("./check-api-compat.sh")
			command.Env = releaseTestEnvironment("API_BASELINE_REBUILD="+value, "GOSUMDB=sum.golang.org", "GOTOOLCHAIN=go1.25.12")
			output, err := command.CombinedOutput()
			if err == nil {
				t.Fatalf("API compatibility gate accepted invalid rebuild flag %q:\n%s", value, output)
			}
			if !strings.Contains(string(output), "API_BASELINE_REBUILD must be 0 or 1") {
				t.Fatalf("invalid rebuild flag failure is not actionable:\n%s", output)
			}
		})
	}
}

func TestAPICompatibilityGateRejectsInvalidNetworkFlag(t *testing.T) {
	for _, value := range []string{"", "banana"} {
		t.Run(value, func(t *testing.T) {
			command := exec.Command("./check-api-compat.sh")
			command.Env = releaseTestEnvironment("API_COMPAT_ALLOW_NETWORK=" + value)
			output, err := command.CombinedOutput()
			if err == nil {
				t.Fatalf("API compatibility gate accepted invalid network flag %q:\n%s", value, output)
			}
			if !strings.Contains(string(output), "API_COMPAT_ALLOW_NETWORK must be 0 or 1") {
				t.Fatalf("invalid network flag failure is not actionable:\n%s", output)
			}
		})
	}
}

func TestAPICompatibilityGateRejectsUncontrolledPathAPIDiff(t *testing.T) {
	directory, bin := fakeAPICompatibilityDirectory(t)
	command := exec.Command("./check-api-compat.sh")
	command.Dir = directory
	command.Env = releaseTestEnvironment(
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"FAKE_API_STATE="+directory,
	)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("API gate accepted an arbitrary PATH apidiff binary:\n%s", output)
	}
	if !strings.Contains(string(output), "APIDIFF_BIN") {
		t.Fatalf("uncontrolled apidiff failure is not actionable:\n%s", output)
	}
}

func TestAPICompatibilityGateRejectsFakeExitZeroShellAPIDiff(t *testing.T) {
	directory, bin := fakeAPICompatibilityDirectory(t)
	apidiffPath := filepath.Join(bin, "apidiff")
	apidiffContents, err := os.ReadFile(apidiffPath)
	if err != nil {
		t.Fatal(err)
	}
	expectedSHA256 := fmt.Sprintf("%x", sha256.Sum256(apidiffContents))
	command := exec.Command("./check-api-compat.sh")
	command.Dir = directory
	command.Env = releaseTestEnvironment(
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"FAKE_API_STATE="+directory,
		"APIDIFF_BIN="+apidiffPath,
		"APIDIFF_EXPECTED_SHA256="+expectedSHA256,
		"FAKE_APIDIFF_BUILD_INFO=unavailable",
	)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("API gate accepted fake exit-0 shell APIDIFF_BIN:\n%s", output)
	}
	if !strings.Contains(string(output), "go version -m") {
		t.Fatalf("fake APIDIFF_BIN rejection does not identify missing build info:\n%s", output)
	}
}

func TestAPICompatibilityGateRecordsControlledOfflineMode(t *testing.T) {
	directory, bin := fakeAPICompatibilityDirectory(t)
	metadata := filepath.Join(directory, "metadata.txt")
	command := exec.Command("./check-api-compat.sh")
	command.Dir = directory
	command.Env = releaseTestEnvironment(
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"FAKE_API_STATE="+directory,
		"APIDIFF_BIN="+filepath.Join(bin, "apidiff"),
		"APIDIFF_EXPECTED_SHA256="+fakeAPIDiffSHA256(t, bin),
		"API_COMPAT_METADATA="+metadata,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("controlled offline API gate failed: %v\n%s", err, output)
	}
	apidiffPath := filepath.Join(bin, "apidiff")
	apidiffPath, err = filepath.EvalSymlinks(apidiffPath)
	if err != nil {
		t.Fatal(err)
	}
	apidiffContents, err := os.ReadFile(apidiffPath)
	if err != nil {
		t.Fatal(err)
	}
	apidiffSHA256 := fmt.Sprintf("%x", sha256.Sum256(apidiffContents))
	for _, evidence := range []string{string(output), readTestFile(t, metadata)} {
		for _, want := range []string{
			"api_compat_network=offline",
			"api_baseline_rebuild=disabled",
			"apidiff_source=controlled-path",
			"apidiff_path=" + apidiffPath,
			"apidiff_sha256=" + apidiffSHA256,
			"apidiff_expected_sha256=" + apidiffSHA256,
			"apidiff_build_path=golang.org/x/exp/cmd/apidiff",
			"apidiff_expected_build_path=golang.org/x/exp/cmd/apidiff",
			"apidiff_build_module=golang.org/x/exp",
			"apidiff_expected_build_module=golang.org/x/exp",
			"apidiff_build_version=v0.0.0-20260709172345-9ea1abe57597",
			"apidiff_expected_build_version=v0.0.0-20260709172345-9ea1abe57597",
			"apidiff_build_commit=9ea1abe57597",
			"apidiff_expected_build_commit=9ea1abe57597",
		} {
			if !strings.Contains(evidence, want) {
				t.Fatalf("API compatibility evidence missing %q:\n%s", want, evidence)
			}
		}
	}
}

func TestAPICompatibilityGateRejectsWrongControlledAPIDiffIdentity(t *testing.T) {
	for _, test := range []struct {
		name      string
		checksum  func(t *testing.T, bin string) string
		buildInfo string
		match     string
	}{
		{
			name:     "wrong checksum",
			checksum: func(*testing.T, string) string { return strings.Repeat("0", sha256.Size*2) },
			match:    "APIDIFF_BIN SHA256 mismatch",
		},
		{
			name:     "wrong module",
			checksum: fakeAPIDiffSHA256,
			buildInfo: "\tpath\tgolang.org/x/exp/cmd/apidiff\n" +
				"\tmod\texample.com/forged\tv0.0.0-20260709172345-9ea1abe57597\th1:forged\n",
			match: "APIDIFF_BIN build module mismatch",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory, bin := fakeAPICompatibilityDirectory(t)
			metadata := filepath.Join(directory, "metadata.txt")
			apidiffPath, err := filepath.EvalSymlinks(filepath.Join(bin, "apidiff"))
			if err != nil {
				t.Fatal(err)
			}
			command := exec.Command("./check-api-compat.sh")
			command.Dir = directory
			command.Env = releaseTestEnvironment(
				"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
				"FAKE_API_STATE="+directory,
				"FAKE_APIDIFF_BUILD_INFO="+test.buildInfo,
				"APIDIFF_BIN="+filepath.Join(bin, "apidiff"),
				"APIDIFF_EXPECTED_SHA256="+test.checksum(t, bin),
				"API_COMPAT_METADATA="+metadata,
			)
			output, err := command.CombinedOutput()
			if err == nil {
				t.Fatalf("API gate accepted %s APIDIFF_BIN identity:\n%s", test.name, output)
			}
			if !strings.Contains(string(output), test.match) {
				t.Fatalf("APIDIFF_BIN rejection missing %q:\n%s", test.match, output)
			}
			evidence := readTestFile(t, metadata)
			for _, want := range []string{
				"api_compat_network=offline",
				"apidiff_source=controlled-path",
				"apidiff_path=" + apidiffPath,
				"apidiff_sha256=" + fakeAPIDiffSHA256(t, bin),
				"apidiff_expected_sha256=" + test.checksum(t, bin),
				"apidiff_build_path=golang.org/x/exp/cmd/apidiff",
				"api_compat_failure_reason=" + test.match,
			} {
				if !strings.Contains(evidence, want) {
					t.Fatalf("failed APIDIFF_BIN identity metadata missing %q:\n%s", want, evidence)
				}
			}
		})
	}
}

func TestAPICompatibilityGateRejectsEmptyOrUntrustedAPIDiffPath(t *testing.T) {
	for _, test := range []struct {
		name  string
		value string
		match string
	}{
		{name: "empty", value: "", match: "APIDIFF_BIN must not be empty"},
		{name: "relative", value: "bin/apidiff", match: "APIDIFF_BIN must be an absolute path"},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory, bin := fakeAPICompatibilityDirectory(t)
			command := exec.Command("./check-api-compat.sh")
			command.Dir = directory
			command.Env = releaseTestEnvironment(
				"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
				"FAKE_API_STATE="+directory,
				"APIDIFF_BIN="+test.value,
			)
			output, err := command.CombinedOutput()
			if err == nil {
				t.Fatalf("API gate accepted untrusted APIDIFF_BIN %q:\n%s", test.value, output)
			}
			if !strings.Contains(string(output), test.match) {
				t.Fatalf("APIDIFF_BIN rejection missing %q:\n%s", test.match, output)
			}
		})
	}
}

func TestAPICompatibilityGateRecordsPinnedOnlineFallback(t *testing.T) {
	directory, bin := fakeAPICompatibilityDirectory(t)
	metadata := filepath.Join(directory, "metadata.txt")
	command := exec.Command("./check-api-compat.sh")
	command.Dir = directory
	command.Env = releaseTestEnvironment(
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"FAKE_API_STATE="+directory,
		"API_COMPAT_ALLOW_NETWORK=1",
		"API_COMPAT_METADATA="+metadata,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("pinned online API fallback failed: %v\n%s", err, output)
	}
	for _, evidence := range []string{string(output), readTestFile(t, metadata)} {
		for _, want := range []string{"api_compat_network=online-opt-in", "apidiff_source=pinned-go-run"} {
			if !strings.Contains(evidence, want) {
				t.Fatalf("online API compatibility evidence missing %q:\n%s", want, evidence)
			}
		}
	}
	if calls := readTestFile(t, filepath.Join(directory, "go-calls")); !strings.Contains(calls, "run golang.org/x/exp/cmd/apidiff@v0.0.0-20260709172345-9ea1abe57597") {
		t.Fatalf("online fallback did not use the pinned apidiff module:\n%s", calls)
	}
}

func TestAPICompatibilityGateRecordsOptInBaselineRebuild(t *testing.T) {
	directory, bin := fakeAPICompatibilityDirectory(t)
	metadata := filepath.Join(directory, "metadata.txt")
	command := exec.Command("./check-api-compat.sh")
	command.Dir = directory
	command.Env = releaseTestEnvironment(
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"FAKE_API_STATE="+directory,
		"APIDIFF_BIN="+filepath.Join(bin, "apidiff"),
		"APIDIFF_EXPECTED_SHA256="+fakeAPIDiffSHA256(t, bin),
		"API_COMPAT_METADATA="+metadata,
		"API_BASELINE_REBUILD=1",
	)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("baseline rebuild fixture unexpectedly completed:\n%s", output)
	}
	for _, evidence := range []string{string(output), readTestFile(t, metadata)} {
		if !strings.Contains(evidence, "api_baseline_rebuild=enabled") {
			t.Fatalf("baseline rebuild mode was not recorded:\n%s", evidence)
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
			directory, bin := fakeAPICompatibilityDirectory(t)
			command := exec.Command("./check-api-compat.sh")
			command.Dir = directory
			command.Env = releaseTestEnvironment("PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "FAKE_API_STATE="+directory, "FAKE_APIDIFF_OUTPUT="+test.output, "APIDIFF_BIN="+filepath.Join(bin, "apidiff"), "APIDIFF_EXPECTED_SHA256="+fakeAPIDiffSHA256(t, bin))
			output, err := command.CombinedOutput()
			if test.wantFailed && err == nil {
				t.Fatalf("API gate accepted incompatible output:\n%s", output)
			}
			if !test.wantFailed && err != nil {
				t.Fatalf("API gate rejected compatible additions: %v\n%s", err, output)
			}
			if calls := readTestFile(t, filepath.Join(directory, "go-calls")); !strings.HasPrefix(strings.TrimSpace(calls), "version -m /") || strings.Count(strings.TrimSpace(calls), "\n") != 0 {
				t.Fatalf("controlled API compatibility path invoked unexpected go command:\n%s", calls)
			}
		})
	}
}

func fakeAPICompatibilityDirectory(t *testing.T) (string, string) {
	t.Helper()
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
	fixtureHash := sha256.Sum256([]byte("fixture"))
	checksum := fmt.Sprintf("%x  v0.9.1.txt\n", fixtureHash)
	if err := os.WriteFile(filepath.Join(directory, "api", "v0.9.1.txt.sha256"), []byte(checksum), 0644); err != nil {
		t.Fatal(err)
	}
	fakeGo := `#!/usr/bin/env bash
printf '%s\n' "$*" >> "${FAKE_API_STATE}/go-calls"
if [[ "${1:-}" == "run" ]]; then
	printf '%s' "${FAKE_APIDIFF_OUTPUT:-}"
	exit 0
fi
if [[ "${1:-} ${2:-}" == "version -m" ]]; then
	if [[ "${FAKE_APIDIFF_BUILD_INFO:-}" == "unavailable" ]]; then
		exit 99
	fi
	if [[ -n "${FAKE_APIDIFF_BUILD_INFO:-}" ]]; then
		printf '%s' "${FAKE_APIDIFF_BUILD_INFO}"
	else
		printf '\tpath\tgolang.org/x/exp/cmd/apidiff\n'
		printf '\tmod\tgolang.org/x/exp\tv0.0.0-20260709172345-9ea1abe57597\th1:fixture\n'
	fi
	exit 0
fi
exit 99
`
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte(fakeGo), 0755); err != nil {
		t.Fatal(err)
	}
	fakeAPIDiff := "#!/usr/bin/env bash\nprintf '%s\\n' \"$*\" >> \"${FAKE_API_STATE}/apidiff-calls\"\nprintf '%s' \"${FAKE_APIDIFF_OUTPUT:-}\"\n"
	if err := os.WriteFile(filepath.Join(bin, "apidiff"), []byte(fakeAPIDiff), 0755); err != nil {
		t.Fatal(err)
	}
	return directory, bin
}

func fakeAPIDiffSHA256(t *testing.T, bin string) string {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(bin, "apidiff"))
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(contents))
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
	vulnDB := filepath.Join(repository, "vulndb")
	if err := os.MkdirAll(vulnDB, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOVULNCHECK_DB", "file://"+vulnDB)
	configureFakeVulnerabilityDatabase(t, vulnDB, state)
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
	if err := os.WriteFile(filepath.Join(repository, "scripts", "check-coverage.sh"), []byte("#!/usr/bin/env bash\nset -euo pipefail\nprintf '%s %s\\n' \"${COVERAGE_MINIMUM:-}\" \"${CRITICAL_COVERAGE_MINIMUM:-}\" > \"${RELEASE_TEST_STATE}/coverage-thresholds\"\ntest -s \"$1\"\n"), 0755); err != nil {
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
if [[ "$1 $2" == "version -m" ]]; then
	case "${3##*/}" in
	staticcheck)
		if [[ "${FAKE_STATICCHECK_BUILD_INFO:-}" == "unavailable" ]]; then exit 99; fi
		if [[ -n "${FAKE_STATICCHECK_BUILD_INFO:-}" ]]; then printf '%s\n' "${FAKE_STATICCHECK_BUILD_INFO}"; else printf 'fixture: go1.25.12\n\tpath\thonnef.co/go/tools/cmd/staticcheck\n\tmod\thonnef.co/go/tools\tv0.7.0\th1:fixture\n\tbuild\t-trimpath=true\n'; fi
		;;
	govulncheck)
		if [[ "${FAKE_GOVULNCHECK_BUILD_INFO:-}" == "unavailable" ]]; then exit 99; fi
		if [[ -n "${FAKE_GOVULNCHECK_BUILD_INFO:-}" ]]; then printf '%s\n' "${FAKE_GOVULNCHECK_BUILD_INFO}"; else printf 'fixture: go1.25.12\n\tpath\tgolang.org/x/vuln/cmd/govulncheck\n\tmod\tgolang.org/x/vuln\tv1.6.0\th1:fixture\n\tbuild\t-trimpath=true\n'; fi
		;;
	esac
	exit 0
fi
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
	fakeTool := "#!/usr/bin/env bash\nexit 0\n"
	for name, contents := range map[string]string{"go": fakeGo, "git": fakeGit, "staticcheck": fakeTool, "govulncheck": fakeTool} {
		if err := os.WriteFile(filepath.Join(repository, "bin", name), []byte(contents), 0755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("STATICCHECK_BIN", filepath.Join(repository, "bin", "staticcheck"))
	t.Setenv("STATICCHECK_EXPECTED_SHA256", fileSHA256(t, filepath.Join(repository, "bin", "staticcheck")))
	t.Setenv("GOVULNCHECK_BIN", filepath.Join(repository, "bin", "govulncheck"))
	t.Setenv("GOVULNCHECK_EXPECTED_SHA256", fileSHA256(t, filepath.Join(repository, "bin", "govulncheck")))
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
	if err := os.MkdirAll(filepath.Join(repository, "vulndb"), 0755); err != nil {
		t.Fatal(err)
	}
	configureFakeVulnerabilityDatabase(t, filepath.Join(repository, "vulndb"), state)
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
printf 'GOPROXY=%s %s\n' "${GOPROXY:-}" "$*" >> "${RC_TEST_STATE}/go-calls"
if [[ "${1:-}" == "version" ]]; then printf '%s\n' 'go version go1.25.12 fixture'; fi
if [[ "${1:-} ${2:-}" == "version -m" ]]; then
	case "${3##*/}" in
	staticcheck)
		if [[ "${FAKE_STATICCHECK_BUILD_INFO:-}" == "unavailable" ]]; then exit 99; fi
		if [[ -n "${FAKE_STATICCHECK_BUILD_INFO:-}" ]]; then printf '%s\n' "${FAKE_STATICCHECK_BUILD_INFO}"; else printf 'fixture: go1.25.12\n\tpath\thonnef.co/go/tools/cmd/staticcheck\n\tmod\thonnef.co/go/tools\tv0.7.0\th1:fixture\n\tbuild\t-trimpath=true\n'; fi
		;;
	govulncheck)
		if [[ "${FAKE_GOVULNCHECK_BUILD_INFO:-}" == "unavailable" ]]; then exit 99; fi
		if [[ -n "${FAKE_GOVULNCHECK_BUILD_INFO:-}" ]]; then printf '%s\n' "${FAKE_GOVULNCHECK_BUILD_INFO}"; else printf 'fixture: go1.25.12\n\tpath\tgolang.org/x/vuln/cmd/govulncheck\n\tmod\tgolang.org/x/vuln\tv1.6.0\th1:fixture\n\tbuild\t-trimpath=true\n'; fi
		;;
	esac
	exit 0
fi
if [[ "${1:-} ${2:-} ${3:-}" == "run honnef.co/go/tools/cmd/staticcheck@v0.7.0 -version" ]]; then printf '%s\n' 'staticcheck fixture'; fi
if [[ "${1:-} ${2:-} ${3:-}" == "run golang.org/x/vuln/cmd/govulncheck@v1.6.0 -version" ]]; then printf '%s\n' 'govulncheck fixture'; fi
exit 0
`
	fakeStaticcheck := `#!/usr/bin/env bash
printf '%s\n' "$*" >> "${RC_TEST_STATE}/staticcheck-calls"
if [[ "${1:-}" == "-version" ]]; then printf '%s\n' 'staticcheck 2026.1 (0.7.0)'; fi
`
	fakeGovulncheck := `#!/usr/bin/env bash
printf '%s\n' "$*" >> "${RC_TEST_STATE}/govulncheck-calls"
if [[ "${1:-}" == "-version" ]]; then printf '%s\n' 'Go: go1.25.12 Scanner: govulncheck@v1.6.0'; fi
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
		elif [[ "${2:-}" == *'^{commit}' ]]; then printf '%s\n' "${RC_TEST_TAG_TARGET:-fixture-commit}"
		fi
		;;
	merge-base)
		if [[ "${2:-}" == "--is-ancestor" ]]; then
			if [[ "${RC_TEST_BASE_NOT_ANCESTOR:-0}" == "1" ]]; then exit 1; fi
		else
			printf '%s\n' fixture-merge-base
		fi
		;;
	cat-file)
		printf '%s\n' "${RC_TEST_TAG_TYPE:-tag}"
		;;
	verify-tag)
		printf '%s\n' '[GNUPG:] VALIDSIG fixture' >&2
		if [[ "${RC_TEST_SIGNATURE_UNTRUSTED:-0}" != "1" ]]; then
			printf '%s\n' '[GNUPG:] TRUST_ULTIMATE 0 pgp' >&2
		fi
		;;
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
	fakeDate := `#!/usr/bin/env bash
printf '%s\n' "${RC_TEST_DATE:-123}"
`
	for name, contents := range map[string]string{"go": fakeGo, "git": fakeGit, "date": fakeDate, "staticcheck": fakeStaticcheck, "govulncheck": fakeGovulncheck} {
		if err := os.WriteFile(filepath.Join(repository, "bin", name), []byte(contents), 0755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("STATICCHECK_BIN", filepath.Join(repository, "bin", "staticcheck"))
	t.Setenv("STATICCHECK_EXPECTED_SHA256", fileSHA256(t, filepath.Join(repository, "bin", "staticcheck")))
	t.Setenv("GOVULNCHECK_BIN", filepath.Join(repository, "bin", "govulncheck"))
	t.Setenv("GOVULNCHECK_EXPECTED_SHA256", fileSHA256(t, filepath.Join(repository, "bin", "govulncheck")))
	return repository, artifact, state
}

func runFakeRC(repository, artifact, state string, extraEnvironment ...string) ([]byte, error) {
	command := exec.Command("./scripts/verify-rc.sh")
	command.Dir = repository
	command.Env = releaseTestEnvironment(
		"PATH="+filepath.Join(repository, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"),
		"RC_ARTIFACT_DIR="+artifact,
		"RC_TEST_STATE="+state,
		"RC_BASE_REF=main",
		"GOVULNCHECK_DB=file://"+filepath.Join(repository, "vulndb"),
	)
	command.Env = append(command.Env, extraEnvironment...)
	return command.CombinedOutput()
}

func releaseTestEnvironment(overrides ...string) []string {
	controlled := map[string]struct{}{
		"API_BASELINE_REBUILD":      {},
		"API_COMPAT_ALLOW_NETWORK":  {},
		"API_COMPAT_METADATA":       {},
		"APIDIFF_BIN":               {},
		"APIDIFF_EXPECTED_SHA256":   {},
		"COVERAGE_MINIMUM":          {},
		"COVERAGE_PROFILE":          {},
		"CRITICAL_COVERAGE_MINIMUM": {},
		"RC_ALLOW_NETWORK":          {},
		"RC_ARTIFACT_DIR":           {},
		"RC_BASE_REF":               {},
		"RC_EXPECTED_VERSION":       {},
		"RC_RELEASE_TAG":            {},
		"RC_REMOTE_HYGIENE":         {},
		"RC_SHUFFLE_TIMEOUT":        {},
		"RELEASE_CHECK_METADATA":    {},
	}
	for _, override := range overrides {
		name, _, found := strings.Cut(override, "=")
		if found {
			controlled[name] = struct{}{}
		}
	}

	environment := make([]string, 0, len(os.Environ())+len(overrides))
	for _, entry := range os.Environ() {
		name, _, found := strings.Cut(entry, "=")
		if found {
			if _, excluded := controlled[name]; excluded {
				continue
			}
		}
		environment = append(environment, entry)
	}
	return append(environment, overrides...)
}

func fileSHA256(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(contents))
}

func configureFakeVulnerabilityDatabase(t *testing.T, database, state string) {
	t.Helper()
	index := filepath.Join(database, "index")
	if err := os.MkdirAll(index, 0755); err != nil {
		t.Fatal(err)
	}
	databaseIndex := filepath.Join(index, "db.json")
	if err := os.WriteFile(databaseIndex, []byte(`{"modified":"fixture"}`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(state, "vulndb.manifest.sha256")
	manifestContents := fileSHA256(t, databaseIndex) + "  index/db.json\n"
	if err := os.WriteFile(manifest, []byte(manifestContents), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOVULNCHECK_DB_MANIFEST", manifest)
	t.Setenv("GOVULNCHECK_DB_MANIFEST_EXPECTED_SHA256", fileSHA256(t, manifest))
}

func systemTrue(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("true")
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(path) {
		t.Fatalf("true path must be absolute: %s", path)
	}
	return path
}

func metadataValue(t *testing.T, path, key string) string {
	t.Helper()
	for _, line := range strings.Split(readTestFile(t, path), "\n") {
		if value, found := strings.CutPrefix(line, key+"="); found {
			return value
		}
	}
	t.Fatalf("metadata %s missing key %s", path, key)
	return ""
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

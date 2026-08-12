//go:build !windows

package scripts

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestCheckCoverageScriptDefaultsToReleaseCandidateThresholds(t *testing.T) {
	contents, err := os.ReadFile("check-coverage.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	for _, want := range []string{
		`${COVERAGE_MINIMUM:-70.0}`,
		`${CRITICAL_COVERAGE_MINIMUM:-80.0}`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("check-coverage.sh missing release threshold %q:\n%s", want, text)
		}
	}
}

func TestCheckCoverageScriptAcceptsProfileAtThreshold(t *testing.T) {
	profile := writeManifestCoverageProfile(t, "", 1, 0, "")

	output, err := runCoverageCheck(profile, "60.0", "60.0")
	if err != nil {
		t.Fatalf("check-coverage.sh error = %v\n%s", err, output)
	}
}

func TestCheckCoverageScriptRejectsProfileBelowThreshold(t *testing.T) {
	profile := writeManifestCoverageProfile(t, "pkg/bear/binding.go", 0, 100, "")

	output, err := runCoverageCheck(profile, "60.0", "1")
	if err == nil {
		t.Fatalf("check-coverage.sh unexpectedly passed:\n%s", output)
	}
}

func TestCheckCoverageScriptRejectsRoundedOverallBoundary(t *testing.T) {
	profile := writeRoundedOverallBoundaryProfile(t, 1399, 601)

	output, err := runCoverageCheck(profile, "70.0", "1")
	if err == nil {
		t.Fatalf("check-coverage.sh accepted unrounded 69.95%% coverage:\n%s", output)
	}
	if !strings.Contains(string(output), "70.0%") || !strings.Contains(string(output), "below 70.0%") {
		t.Fatalf("rounded display should remain actionable without changing comparison:\n%s", output)
	}
}

func TestCheckCoverageScriptRejectsRoundedCriticalBoundary(t *testing.T) {
	profile := writeManifestCoverageProfile(t, "pkg/bear/binding.go", 1599, 401, "")

	output, err := runCoverageCheck(profile, "1", "80.0")
	if err == nil {
		t.Fatalf("check-coverage.sh accepted unrounded 79.95%% critical coverage:\n%s", output)
	}
	if !strings.Contains(string(output), "critical coverage binding 80.0% is below 80.0%") {
		t.Fatalf("critical boundary failure is not actionable:\n%s", output)
	}
}

func TestCheckCoverageScriptRejectsInvalidThresholds(t *testing.T) {
	profile := writeManifestCoverageProfile(t, "", 1, 0, "")
	for _, test := range []struct {
		name     string
		minimum  string
		critical string
	}{
		{name: "zero overall", minimum: "0", critical: "80"},
		{name: "negative overall", minimum: "-1", critical: "80"},
		{name: "NaN overall", minimum: "NaN", critical: "80"},
		{name: "text overall", minimum: "disabled", critical: "80"},
		{name: "over 100 overall", minimum: "100.1", critical: "80"},
		{name: "zero critical", minimum: "70", critical: "0"},
		{name: "negative critical", minimum: "70", critical: "-1"},
		{name: "NaN critical", minimum: "70", critical: "NaN"},
		{name: "text critical", minimum: "70", critical: "disabled"},
		{name: "over 100 critical", minimum: "70", critical: "100.1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			output, err := runCoverageCheck(profile, test.minimum, test.critical)
			if err == nil {
				t.Fatalf("check-coverage.sh accepted invalid thresholds overall=%q critical=%q:\n%s", test.minimum, test.critical, output)
			}
			if !strings.Contains(string(output), "must be a finite decimal greater than 0 and at most 100") {
				t.Fatalf("threshold failure is not actionable:\n%s", output)
			}
		})
	}
}

func TestCheckCoverageScriptAcceptsThresholdEndpoints(t *testing.T) {
	profile := writeManifestCoverageProfile(t, "", 1, 0, "")
	for _, threshold := range []string{"0.0001", "100", "100.0"} {
		output, err := runCoverageCheck(profile, threshold, threshold)
		if err != nil {
			t.Fatalf("check-coverage.sh rejected valid threshold %q: %v\n%s", threshold, err, output)
		}
	}
}

func TestCheckCoverageScriptAcceptsSupportedProfileModes(t *testing.T) {
	setProfile := writeManifestCoverageProfile(t, "", 1, 0, "")
	contents := readTestFile(t, setProfile)
	for _, mode := range []string{"set", "count", "atomic"} {
		profile := filepath.Join(t.TempDir(), "coverage.out")
		if err := os.WriteFile(profile, []byte(strings.Replace(contents, "mode: set", "mode: "+mode, 1)), 0644); err != nil {
			t.Fatal(err)
		}
		output, err := runCoverageCheck(profile, "1", "1")
		if err != nil {
			t.Fatalf("check-coverage.sh rejected mode %q: %v\n%s", mode, err, output)
		}
	}
}

func TestCheckCoverageScriptRejectsMalformedProfiles(t *testing.T) {
	valid := readTestFile(t, writeManifestCoverageProfile(t, "", 1, 0, ""))
	validLine := firstCoverageDataLine(t, valid)
	for _, test := range []struct {
		name    string
		profile string
	}{
		{name: "unknown mode", profile: strings.Replace(valid, "mode: set", "mode: bogus", 1)},
		{name: "no statements", profile: "mode: set\n"},
		{name: "missing mode", profile: strings.TrimPrefix(valid, "mode: set\n")},
		{name: "extra field", profile: strings.Replace(valid, validLine, validLine+" extra", 1)},
		{name: "zero statements", profile: strings.Replace(valid, validLine, replaceCoverageCounts(t, validLine, "0", "1"), 1)},
		{name: "negative statements", profile: strings.Replace(valid, validLine, replaceCoverageCounts(t, validLine, "-1", "1"), 1)},
		{name: "negative count", profile: strings.Replace(valid, validLine, replaceCoverageCounts(t, validLine, "1", "-1"), 1)},
		{name: "decimal count", profile: strings.Replace(valid, validLine, replaceCoverageCounts(t, validLine, "1", "1.5"), 1)},
		{name: "invalid position", profile: strings.Replace(valid, ":1.1,1.2 ", ":line.1,1.2 ", 1)},
		{name: "zero position", profile: strings.Replace(valid, ":1.1,1.2 ", ":0.1,1.2 ", 1)},
		{name: "blank data line", profile: strings.Replace(valid, validLine+"\n", validLine+"\n\n", 1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			profile := filepath.Join(t.TempDir(), "coverage.out")
			if err := os.WriteFile(profile, []byte(test.profile), 0644); err != nil {
				t.Fatal(err)
			}
			output, err := runCoverageCheck(profile, "1", "1")
			if err == nil {
				t.Fatalf("check-coverage.sh accepted malformed profile:\n%s", output)
			}
			if !strings.Contains(string(output), "malformed coverage profile") {
				t.Fatalf("profile failure is not actionable:\n%s", output)
			}
		})
	}
}

func TestCriticalCoverageManifestCoversAuditedProductionFiles(t *testing.T) {
	manifest := readCriticalCoverageManifest(t)
	want := map[string][]string{
		"handler": {
			"pkg/bear/bear.go",
			"pkg/bear/fairing.go",
			"pkg/bear/handler.go",
			"pkg/bear/responder.go",
		},
		"lifecycle": {
			"pkg/bear/bear.go",
			"pkg/bear/lifecycle.go",
		},
	}
	for label, files := range want {
		sort.Strings(files)
		got := append([]string(nil), manifest[label]...)
		sort.Strings(got)
		if strings.Join(got, "\n") != strings.Join(files, "\n") {
			t.Fatalf("critical coverage %s files = %v, want %v", label, got, files)
		}
	}

	command := exec.Command("go", "list", "-f", `{{range .GoFiles}}{{println .}}{{end}}`, "../internal/scaffold")
	command.Env = append(os.Environ(), "GOSUMDB=sum.golang.org", "GOTOOLCHAIN=go1.25.12")
	output, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	var production []string
	for _, filename := range strings.Fields(string(output)) {
		production = append(production, filepath.ToSlash(filepath.Join("internal/scaffold", filename)))
	}
	sort.Strings(production)
	got := append([]string(nil), manifest["scaffold"]...)
	sort.Strings(got)
	if strings.Join(got, "\n") != strings.Join(production, "\n") {
		t.Fatalf("critical coverage scaffold files = %v, current platform production GoFiles = %v", got, production)
	}
}

func TestCriticalCoverageManifestAuditRejectsNewRuleOwnedFile(t *testing.T) {
	repository, manifest, profile := writeSyntheticCoverageRepository(t)
	output, err := runCoverageCheckWithRepository(profile, "1", "1", repository, manifest)
	if err != nil {
		t.Fatalf("synthetic manifest baseline failed: %v\n%s", err, output)
	}

	writeGoFixture(t, repository, "pkg/bear/handler_extra.go")
	output, err = runCoverageCheckWithRepository(profile, "1", "1", repository, manifest)
	if err == nil {
		t.Fatalf("manifest audit allowed a new handler*.go file to escape:\n%s", output)
	}
	if !strings.Contains(string(output), "handler") || !strings.Contains(string(output), "pkg/bear/handler_extra.go") {
		t.Fatalf("manifest escape failure is not actionable:\n%s", output)
	}
}

func TestCriticalCoverageManifestAuditFailsClosedWhenGoListFails(t *testing.T) {
	repository, manifest, profile := writeSyntheticCoverageRepository(t)
	if err := os.RemoveAll(filepath.Join(repository, "internal", "scaffold")); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(readTestFile(t, manifest), "\n")
	kept := lines[:0]
	for _, line := range lines {
		if !strings.HasPrefix(line, "scaffold ") {
			kept = append(kept, line)
		}
	}
	if err := os.WriteFile(manifest, []byte(strings.Join(kept, "\n")), 0644); err != nil {
		t.Fatal(err)
	}

	output, err := runCoverageCheckWithRepository(profile, "1", "1", repository, manifest)
	if err == nil {
		t.Fatalf("manifest audit ignored go list failure:\n%s", output)
	}
}

func TestCriticalCoverageManifestAuditRejectsExtraWrongAndDuplicateEntries(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(string) string
	}{
		{name: "extra", mutate: func(contents string) string { return contents + "handler pkg/bear/value.go\n" }},
		{name: "wrong group", mutate: func(contents string) string {
			return strings.Replace(contents, "binding pkg/bear/binding.go", "handler pkg/bear/binding.go", 1)
		}},
		{name: "duplicate", mutate: func(contents string) string { return contents + "binding pkg/bear/binding.go\n" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository, manifest, profile := writeSyntheticCoverageRepository(t)
			contents := test.mutate(readTestFile(t, manifest))
			if err := os.WriteFile(manifest, []byte(contents), 0644); err != nil {
				t.Fatal(err)
			}
			output, err := runCoverageCheckWithRepository(profile, "1", "1", repository, manifest)
			if err == nil {
				t.Fatalf("manifest audit accepted %s entry:\n%s", test.name, output)
			}
		})
	}
}

func TestCheckCoverageScriptRejectsMissingManifestFile(t *testing.T) {
	profile := writeManifestCoverageProfile(t, "", 1, 0, "pkg/bear/responder.go")

	output, err := runCoverageCheck(profile, "1", "80.0")
	if err == nil {
		t.Fatalf("check-coverage.sh accepted a profile missing responder.go:\n%s", output)
	}
	if !strings.Contains(string(output), "handler") || !strings.Contains(string(output), "pkg/bear/responder.go") {
		t.Fatalf("missing-file failure is not actionable:\n%s", output)
	}
}

func TestCheckCoverageScriptRejectsCriticalChainBelowThreshold(t *testing.T) {
	profile := writeCriticalCoverageProfile(t, "pkg/bear/binding.go")

	output, err := runCoverageCheck(profile, "70.0", "80.0")
	if err == nil {
		t.Fatalf("check-coverage.sh accepted under-covered binding chain:\n%s", output)
	}
	if !strings.Contains(string(output), "binding") || !strings.Contains(string(output), "below 80.0%") {
		t.Fatalf("critical coverage failure is not actionable:\n%s", output)
	}
}

func writeManifestCoverageProfile(t *testing.T, weightedFile string, covered, uncovered int, omitted string) string {
	t.Helper()
	manifest := readCriticalCoverageManifest(t)
	dir := t.TempDir()
	var profile strings.Builder
	profile.WriteString("mode: set\n")
	seen := make(map[string]struct{})
	for _, files := range manifest {
		for _, name := range files {
			if name == omitted {
				continue
			}
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			path := filepath.Join(dir, name)
			if name == weightedFile {
				fmt.Fprintf(&profile, "%s:1.1,1.2 %d 1\n%s:2.1,2.2 %d 0\n", path, covered, path, uncovered)
				continue
			}
			fmt.Fprintf(&profile, "%s:1.1,1.2 1 1\n", path)
		}
	}
	profilePath := filepath.Join(dir, "coverage.out")
	if err := os.WriteFile(profilePath, []byte(profile.String()), 0644); err != nil {
		t.Fatal(err)
	}
	return profilePath
}

func writeRoundedOverallBoundaryProfile(t *testing.T, covered, uncovered int) string {
	t.Helper()
	manifest := readCriticalCoverageManifest(t)
	unique := make(map[string]struct{})
	for _, files := range manifest {
		for _, name := range files {
			unique[name] = struct{}{}
		}
	}
	weightedCovered := covered - (len(unique) - 1)
	if weightedCovered <= 0 {
		t.Fatalf("covered fixture weight %d is too small for %d manifest files", covered, len(unique))
	}
	return writeManifestCoverageProfile(t, "pkg/bear/binding.go", weightedCovered, uncovered, "")
}

func readCriticalCoverageManifest(t *testing.T) map[string][]string {
	t.Helper()
	file, err := os.Open("critical-coverage-files.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	manifest := make(map[string][]string)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			t.Fatalf("invalid critical coverage manifest line %q", line)
		}
		manifest[fields[0]] = append(manifest[fields[0]], fields[1])
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return manifest
}

func writeCriticalCoverageProfile(t *testing.T, underCovered string) string {
	t.Helper()
	return writeManifestCoverageProfile(t, underCovered, 1, 1, "")
}

func firstCoverageDataLine(t *testing.T, profile string) string {
	t.Helper()
	lines := strings.Split(profile, "\n")
	if len(lines) < 2 || lines[1] == "" {
		t.Fatalf("profile has no data line: %q", profile)
	}
	return lines[1]
}

func replaceCoverageCounts(t *testing.T, line, statements, count string) string {
	t.Helper()
	fields := strings.Fields(line)
	if len(fields) != 3 {
		t.Fatalf("invalid fixture coverage line %q", line)
	}
	return fields[0] + " " + statements + " " + count
}

func writeSyntheticCoverageRepository(t *testing.T) (string, string, string) {
	t.Helper()
	repository := t.TempDir()
	if err := os.WriteFile(filepath.Join(repository, "go.mod"), []byte("module example.com/coverage-fixture\n\ngo 1.25.0\n"), 0644); err != nil {
		t.Fatal(err)
	}
	manifestEntries := map[string][]string{
		"handler":        {"pkg/bear/bear.go", "pkg/bear/fairing.go", "pkg/bear/handler.go", "pkg/bear/responder.go"},
		"lifecycle":      {"pkg/bear/bear.go", "pkg/bear/lifecycle.go"},
		"auth":           {"pkg/bear/auth_token.go", "pkg/bear/jwt.go", "pkg/bear/jwt_fairing.go"},
		"binding":        {"pkg/bear/binding.go"},
		"errors":         {"pkg/bear/error.go", "pkg/bear/http_error.go"},
		"config-loader":  {"pkg/bear/config_loader.go"},
		"migration-lock": {"pkg/bear/migration.go"},
		"cron-lock":      {"pkg/bear/cron_lock.go"},
		"cli":            {"internal/cli/gen.go", "internal/cli/new.go", "internal/cli/root.go"},
		"scaffold":       {"internal/scaffold/embed.go"},
	}
	var manifestContents strings.Builder
	var profileContents strings.Builder
	profileContents.WriteString("mode: set\n")
	labels := make([]string, 0, len(manifestEntries))
	for label := range manifestEntries {
		labels = append(labels, label)
	}
	sort.Strings(labels)
	seen := make(map[string]struct{})
	for _, label := range labels {
		for _, path := range manifestEntries[label] {
			fmt.Fprintf(&manifestContents, "%s %s\n", label, path)
			writeGoFixture(t, repository, path)
			if _, exists := seen[path]; exists {
				continue
			}
			seen[path] = struct{}{}
			fmt.Fprintf(&profileContents, "%s:1.1,1.2 1 1\n", filepath.ToSlash(filepath.Join(repository, path)))
		}
	}
	manifest := filepath.Join(repository, "critical-coverage-files.txt")
	if err := os.WriteFile(manifest, []byte(manifestContents.String()), 0644); err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(repository, "coverage.out")
	if err := os.WriteFile(profile, []byte(profileContents.String()), 0644); err != nil {
		t.Fatal(err)
	}
	return repository, manifest, profile
}

func writeGoFixture(t *testing.T, repository, path string) {
	t.Helper()
	fullPath := filepath.Join(repository, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		t.Fatal(err)
	}
	packageName := filepath.Base(filepath.Dir(path))
	if err := os.WriteFile(fullPath, []byte("package "+packageName+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
}

func runCoverageCheck(profile, minimum, criticalMinimum string) ([]byte, error) {
	return runCoverageCheckWithRepository(profile, minimum, criticalMinimum, "", "")
}

func runCoverageCheckWithRepository(profile, minimum, criticalMinimum, repository, manifest string) ([]byte, error) {
	command := exec.Command("./check-coverage.sh", profile)
	command.Env = append(os.Environ(),
		"COVERAGE_MINIMUM="+minimum,
		"CRITICAL_COVERAGE_MINIMUM="+criticalMinimum,
	)
	if repository != "" {
		command.Env = append(command.Env,
			"CRITICAL_COVERAGE_REPOSITORY_ROOT="+repository,
			"CRITICAL_COVERAGE_MANIFEST="+manifest,
		)
	}
	return command.CombinedOutput()
}

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
	profile := writeCoverageProfile(t, 1, 0)

	output, err := runCoverageCheck(profile, "60.0", "0")
	if err != nil {
		t.Fatalf("check-coverage.sh error = %v\n%s", err, output)
	}
}

func TestCheckCoverageScriptRejectsProfileBelowThreshold(t *testing.T) {
	profile := writeCoverageProfile(t, 0, 1)

	output, err := runCoverageCheck(profile, "60.0", "0")
	if err == nil {
		t.Fatalf("check-coverage.sh unexpectedly passed:\n%s", output)
	}
}

func TestCheckCoverageScriptRejectsRoundedOverallBoundary(t *testing.T) {
	profile := writeWeightedCoverageProfile(t, "sample.go", 1399, 601)

	output, err := runCoverageCheck(profile, "70.0", "0")
	if err == nil {
		t.Fatalf("check-coverage.sh accepted unrounded 69.95%% coverage:\n%s", output)
	}
	if !strings.Contains(string(output), "70.0%") || !strings.Contains(string(output), "below 70.0%") {
		t.Fatalf("rounded display should remain actionable without changing comparison:\n%s", output)
	}
}

func TestCheckCoverageScriptRejectsRoundedCriticalBoundary(t *testing.T) {
	profile := writeManifestCoverageProfile(t, "pkg/bear/binding.go", 1599, 401, "")

	output, err := runCoverageCheck(profile, "0", "80.0")
	if err == nil {
		t.Fatalf("check-coverage.sh accepted unrounded 79.95%% critical coverage:\n%s", output)
	}
	if !strings.Contains(string(output), "critical coverage binding 80.0% is below 80.0%") {
		t.Fatalf("critical boundary failure is not actionable:\n%s", output)
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

func TestCheckCoverageScriptRejectsMissingManifestFile(t *testing.T) {
	profile := writeManifestCoverageProfile(t, "", 1, 0, "pkg/bear/responder.go")

	output, err := runCoverageCheck(profile, "0", "80.0")
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

func writeCoverageProfile(t *testing.T, firstCount, secondCount int) string {
	t.Helper()
	dir := t.TempDir()
	source := filepath.Join(dir, "sample.go")
	if err := os.WriteFile(source, []byte("package sample\n\nfunc first() {}\nfunc second() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(dir, "coverage.out")
	contents := fmt.Sprintf("mode: set\n%s:3.1,3.14 3 %d\n%s:4.1,4.15 2 %d\n", source, firstCount, source, secondCount)
	if err := os.WriteFile(profile, []byte(contents), 0644); err != nil {
		t.Fatal(err)
	}
	return profile
}

func writeWeightedCoverageProfile(t *testing.T, name string, covered, uncovered int) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("package sample\nfunc covered() {}\nfunc uncovered() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	contents := fmt.Sprintf("mode: set\n%s:2.1,2.18 %d 1\n%s:3.1,3.20 %d 0\n", path, covered, path, uncovered)
	profile := filepath.Join(dir, "coverage.out")
	if err := os.WriteFile(profile, []byte(contents), 0644); err != nil {
		t.Fatal(err)
	}
	return profile
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

func runCoverageCheck(profile, minimum, criticalMinimum string) ([]byte, error) {
	command := exec.Command("./check-coverage.sh", profile)
	command.Env = append(os.Environ(),
		"COVERAGE_MINIMUM="+minimum,
		"CRITICAL_COVERAGE_MINIMUM="+criticalMinimum,
	)
	return command.CombinedOutput()
}

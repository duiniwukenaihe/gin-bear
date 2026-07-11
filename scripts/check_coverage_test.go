package scripts

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

func writeCriticalCoverageProfile(t *testing.T, underCovered string) string {
	t.Helper()
	dir := t.TempDir()
	paths := []string{
		"pkg/bear/handler.go",
		"pkg/bear/binding.go",
		"pkg/bear/error.go",
		"pkg/bear/config_loader.go",
		"pkg/bear/lifecycle.go",
		"pkg/bear/auth_token.go",
		"pkg/bear/migration.go",
		"pkg/bear/cron_lock.go",
		"internal/cli/root.go",
		"internal/scaffold/embed.go",
	}
	var profile strings.Builder
	profile.WriteString("mode: set\n")
	for _, name := range paths {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("package sample\n\nfunc first() {}\nfunc second() {}\n"), 0644); err != nil {
			t.Fatal(err)
		}
		secondCount := 1
		if name == underCovered {
			secondCount = 0
		}
		fmt.Fprintf(&profile, "%s:3.1,3.14 1 1\n%s:4.1,4.15 1 %d\n", path, path, secondCount)
	}
	profilePath := filepath.Join(dir, "coverage.out")
	if err := os.WriteFile(profilePath, []byte(profile.String()), 0644); err != nil {
		t.Fatal(err)
	}
	return profilePath
}

func runCoverageCheck(profile, minimum, criticalMinimum string) ([]byte, error) {
	command := exec.Command("./check-coverage.sh", profile)
	command.Env = append(os.Environ(),
		"COVERAGE_MINIMUM="+minimum,
		"CRITICAL_COVERAGE_MINIMUM="+criticalMinimum,
	)
	return command.CombinedOutput()
}

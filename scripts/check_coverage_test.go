package scripts

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestCheckCoverageScriptAcceptsProfileAtThreshold(t *testing.T) {
	profile := writeCoverageProfile(t, 1, 0)

	output, err := runCoverageCheck(profile, "60.0")
	if err != nil {
		t.Fatalf("check-coverage.sh error = %v\n%s", err, output)
	}
}

func TestCheckCoverageScriptRejectsProfileBelowThreshold(t *testing.T) {
	profile := writeCoverageProfile(t, 0, 1)

	output, err := runCoverageCheck(profile, "60.0")
	if err == nil {
		t.Fatalf("check-coverage.sh unexpectedly passed:\n%s", output)
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

func runCoverageCheck(profile, minimum string) ([]byte, error) {
	command := exec.Command("./check-coverage.sh", profile)
	command.Env = append(os.Environ(), "COVERAGE_MINIMUM="+minimum)
	return command.CombinedOutput()
}

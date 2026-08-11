package cmd

import (
	"errors"
	"os"
	"os/exec"
	"testing"
)

func TestExecutePreservesNonzeroCommandStatus(t *testing.T) {
	if os.Getenv("BEAR_TEST_EXECUTE_HELPER") == "1" {
		os.Args = []string{"bear-cli", "gen", "unsupported", "widget"}
		Execute()
		os.Exit(0)
	}

	command := exec.Command(os.Args[0], "-test.run=^TestExecutePreservesNonzeroCommandStatus$")
	command.Env = append(os.Environ(), "BEAR_TEST_EXECUTE_HELPER=1")
	err := command.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("deprecated Execute status error = %v, want nonzero exit", err)
	}
	if exitErr.ExitCode() != 1 {
		t.Fatalf("deprecated Execute exit code = %d, want 1", exitErr.ExitCode())
	}
}

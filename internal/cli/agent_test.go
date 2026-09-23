package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func chdir(t *testing.T, dir string) {
	t.Helper()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(previous); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})
}

func TestAgentInitCreatesEntryAndIgnore(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/agentinit\n\ngo 1.25.14\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	chdir(t, dir)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := Execute([]string{"agent", "init"}, &stdout, &stderr); code != 0 {
		t.Fatalf("agent init exit = %d stderr=%q", code, stderr.String())
	}
	entry, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if err != nil {
		t.Fatalf("AGENTS.md not created: %v", err)
	}
	if !strings.Contains(string(entry), "example.com/agentinit") {
		t.Fatalf("AGENTS.md does not name the module:\n%s", entry)
	}
	ignore, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatalf(".gitignore not created: %v", err)
	}
	for _, want := range []string{"AGENTS.md", "agent.md"} {
		found := false
		for _, line := range strings.Split(string(ignore), "\n") {
			if strings.TrimSpace(line) == want {
				found = true
			}
		}
		if !found {
			t.Fatalf(".gitignore missing %s:\n%s", want, ignore)
		}
	}
	if !strings.Contains(stdout.String(), "not a git checkout") {
		t.Fatalf("expected non-repo note, got stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestAgentInitNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/agentkeep\n\ngo 1.25.14\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	const existing = "# hand-written entry\n"
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}
	chdir(t, dir)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := Execute([]string{"agent", "init"}, &stdout, &stderr); code != 0 {
		t.Fatalf("agent init exit = %d stderr=%q", code, stderr.String())
	}
	kept, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(kept) != existing {
		t.Fatalf("AGENTS.md overwritten:\n%s", kept)
	}
	if !strings.Contains(stdout.String(), "kept as-is") {
		t.Fatalf("expected kept-as-is note, got %q", stdout.String())
	}
}

func TestAgentInitPreservesGitignore(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/agentignore\n\ngo 1.25.14\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// No trailing newline on purpose; entries must still merge cleanly once.
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("bin/\nAGENTS.md"), 0o644); err != nil {
		t.Fatal(err)
	}
	chdir(t, dir)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := Execute([]string{"agent", "init"}, &stdout, &stderr); code != 0 {
		t.Fatalf("agent init exit = %d stderr=%q", code, stderr.String())
	}
	ignore, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(ignore), "\n"), "\n")
	counts := map[string]int{}
	for _, line := range lines {
		counts[line]++
	}
	if counts["bin/"] != 1 || counts["AGENTS.md"] != 1 || counts["agent.md"] != 1 {
		t.Fatalf(".gitignore merge wrong:\n%s", ignore)
	}
}

func execLookPath(file string) (string, error) {
	return exec.LookPath(file)
}

func execGitInit(dir string) (string, error) {
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestAgentInitVerifiesGitIgnore(t *testing.T) {
	if _, err := execLookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/agentgit\n\ngo 1.25.14\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := execGitInit(dir); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	chdir(t, dir)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := Execute([]string{"agent", "init"}, &stdout, &stderr); code != 0 {
		t.Fatalf("agent init exit = %d stderr=%q", code, stderr.String())
	}
	combined := stdout.String() + stderr.String()
	for _, want := range []string{"AGENTS.md is ignored", "agent.md is ignored", "untracked"} {
		if !strings.Contains(combined, want) {
			t.Fatalf("missing %q in output:\n%s", want, combined)
		}
	}
}

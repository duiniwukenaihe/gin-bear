package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// agentFileName is the local entry every tool reads; agentRuleFile holds the
// shared rules it points at. Both stay untracked by design.
const (
	agentFileName     = "AGENTS.md"
	agentRuleFileName = "agent.md"
)

func agentCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "agent",
		Short: "Local development-agent helpers",
	}
	command.AddCommand(agentInitCommand())
	return command
}

func agentInitCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Create local AGENTS.md and ignore rules (never overwrites)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			currentDirectory, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("resolve working directory: %w", err)
			}
			root, err := nearestGoModRoot(currentDirectory)
			if err != nil {
				return err
			}
			module := filepath.Base(root)
			if parsed, err := goModModule(filepath.Join(root, "go.mod")); err == nil && parsed != "" {
				module = parsed
			}

			entryPath := filepath.Join(root, agentFileName)
			if _, err := os.Stat(entryPath); err == nil {
				fmt.Fprintf(cmd.OutOrStdout(), "%s already exists; kept as-is (no overwrite)\n", agentFileName)
				suggestAgentEntryDiff(cmd, entryPath, renderAgentEntry(module))
			} else if !os.IsNotExist(err) {
				return fmt.Errorf("inspect %s: %w", agentFileName, err)
			} else {
				if err := os.WriteFile(entryPath, []byte(renderAgentEntry(module)), 0o644); err != nil {
					return fmt.Errorf("write %s: %w", agentFileName, err)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "created %s (local only, do not commit)\n", agentFileName)
			}

			if err := ensureAgentIgnore(root, cmd); err != nil {
				return err
			}
			verifyAgentIgnore(cmd, root)
			return nil
		},
	}
}

func renderAgentEntry(module string) string {
	return "# " + module + " — local agent entry (do not commit)\n" +
		"\n" +
		"This file is machine-generated scaffolding for development tools.\n" +
		"Authoritative rules live in `" + agentRuleFileName + "`; project facts live in\n" +
		"`docs/development.md`, `docs/architecture.md`, and `docs/recipes/`.\n" +
		"\n" +
		"## Commands\n" +
		"\n" +
		"- `go test ./... -count=1` — unit suite\n" +
		"- `RC_ALLOW_NETWORK=1 API_COMPAT_ALLOW_NETWORK=1 make verify` — full gate\n" +
		"- `scripts/test-integration.sh` — real-dependency acceptance\n" +
		"- `bear doctor [--format json] [--probe]` — static diagnosis\n" +
		"\n" +
		"## Discipline\n" +
		"\n" +
		"- One work package per diff; repro before fix; report real evidence.\n" +
		"- Never commit `" + agentFileName + "` or `" + agentRuleFileName + "`;\n" +
		"  never `git add -f` ignored files.\n" +
		"- Never print secrets, tokens, or DSNs.\n"
}

// suggestAgentEntryDiff prints a hint when the existing entry drifts from the
// generated one, without touching the file.
func suggestAgentEntryDiff(cmd *cobra.Command, path, generated string) {
	existing, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: cannot re-read %s: %v\n", agentFileName, err)
		return
	}
	if string(existing) == generated {
		fmt.Fprintf(cmd.OutOrStdout(), "%s matches the generated entry\n", agentFileName)
		return
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s differs from the generated entry; merge manually if the drift is unintentional\n", agentFileName)
}

// ensureAgentIgnore appends the two local filenames to .gitignore when absent,
// preserving everything already there. It never removes or rewrites entries.
func ensureAgentIgnore(root string, cmd *cobra.Command) error {
	path := filepath.Join(root, ".gitignore")
	contents, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read .gitignore: %w", err)
	}
	lines := []string{}
	if len(contents) > 0 {
		lines = strings.Split(string(contents), "\n")
	}
	present := map[string]bool{}
	for _, line := range lines {
		present[strings.TrimSpace(line)] = true
	}
	changed := false
	for _, name := range []string{agentFileName, agentRuleFileName} {
		if !present[name] {
			lines = append(lines, name)
			changed = true
		}
	}
	if !changed {
		fmt.Fprintf(cmd.OutOrStdout(), ".gitignore already ignores local agent files\n")
		return nil
	}
	output := strings.Join(lines, "\n")
	if !strings.HasSuffix(output, "\n") {
		output += "\n"
	}
	if err := os.WriteFile(path, []byte(output), 0o644); err != nil {
		return fmt.Errorf("write .gitignore: %w", err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), ".gitignore now ignores local agent files\n")
	return nil
}

// execGit runs git in root and reports its exit code. A missing git binary
// or any failure is a plain non-zero, never a panic.
func execGit(root string, args ...string) int {
	if _, err := exec.LookPath("git"); err != nil {
		return 127
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		return 1
	}
	return 0
}

func execGitOutput(root string, args ...string) int {
	return execGit(root, args...)
}

// verifyAgentIgnore reports git ignore/untracked state. It never stages,
// commits, or force-adds anything.
func verifyAgentIgnore(cmd *cobra.Command, root string) {
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "not a git checkout; skipped ignore verification\n")
		return
	}
	for _, name := range []string{agentFileName, agentRuleFileName} {
		check := execGit(root, "check-ignore", "-q", name)
		if check != 0 {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s is not ignored by git\n", name)
			continue
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s is ignored\n", name)
	}
	if out := execGitOutput(root, "ls-files", "--error-unmatch", agentFileName, agentRuleFileName); out == 0 {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: a local agent file is tracked; remove it from the index\n")
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "local agent files are untracked\n")
	}
}

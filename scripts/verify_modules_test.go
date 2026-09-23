//go:build !windows

package scripts

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	agentModule = "extensions/agent"
	mcpModule   = "tools/bear-mcp"
)

func TestVerifyModulesScriptCoversEveryNestedModule(t *testing.T) {
	content, err := os.ReadFile("verify-modules.sh")
	if err != nil {
		t.Fatalf("read verify-modules.sh: %v", err)
	}
	text := string(content)
	for _, want := range []string{
		"extensions/agent",
		"tools/bear-mcp",
		"go test ./... -count=1",
		"go test -race ./... -count=1",
		"go vet ./...",
		"staticcheck ./...",
		"govulncheck ./...",
		"is required in offline mode",
		"nested-module verification FAILED",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("verify-modules.sh missing %q:\n%s", want, text)
		}
	}
	if !strings.Contains(text, "nested module not found") {
		t.Fatalf("verify-modules.sh must fail when a nested module is absent:\n%s", text)
	}
}

func TestVerifyAllScriptRunsRootAndNestedGates(t *testing.T) {
	info, err := os.Stat("verify-all.sh")
	if err != nil {
		t.Fatalf("verify-all.sh should exist: %v", err)
	}
	if info.Mode()&0111 == 0 {
		t.Fatalf("verify-all.sh should be executable, mode=%s", info.Mode())
	}
	text := readTestFile(t, "verify-all.sh")
	for _, want := range []string{"scripts/release-check.sh", "scripts/verify-modules.sh"} {
		if !strings.Contains(text, want) {
			t.Fatalf("verify-all.sh missing %q:\n%s", want, text)
		}
	}
}

func TestVerifyRCGatesNestedModules(t *testing.T) {
	if !strings.Contains(readTestFile(t, "verify-rc.sh"), "scripts/verify-modules.sh") {
		t.Fatal("verify-rc.sh must gate the nested modules")
	}
}

func TestIntegrationWorkflowsRequireEnginesAndNestedModules(t *testing.T) {
	for _, path := range []string{"../.github/workflows/integration.yml", "../.github/workflows/release.yml"} {
		text := readTestFile(t, path)
		for _, want := range []string{
			"BEAR_INTEGRATION_REQUIRE: pg,mysql,redis",
			"scripts/verify-modules.sh",
			"BEAR_AGENT_PG_DSN",
		} {
			if !strings.Contains(text, want) {
				t.Fatalf("%s missing %q:\n%s", path, want, text)
			}
		}
	}
}

func TestVerifyModulesScriptPassesWhenEveryModuleIsGreen(t *testing.T) {
	repository, state := fakeModulesRepository(t)
	output, err := runVerifyModules(t, repository, state)
	if err != nil {
		t.Fatalf("verify-modules.sh failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "nested-module verification PASSED") {
		t.Fatalf("missing success marker:\n%s", output)
	}
	goCalls := readTestFile(t, filepath.Join(state, "go-calls"))
	for _, module := range []string{agentModule, mcpModule} {
		if !strings.Contains(goCalls, filepath.Join(repository, module)) {
			t.Fatalf("nested module %s was not exercised:\n%s", module, goCalls)
		}
	}
}

func TestVerifyModulesScriptFailsWhenAModuleFails(t *testing.T) {
	repository, state := fakeModulesRepository(t)
	output, err := runVerifyModules(t, repository, state, "MODULES_TEST_FAIL_MODULE="+mcpModule)
	if err == nil {
		t.Fatalf("verify-modules.sh must fail when a nested module test fails:\n%s", output)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() == 0 {
		t.Fatalf("expected non-zero exit, got %v:\n%s", err, output)
	}
	if !strings.Contains(string(output), "nested-module verification FAILED") {
		t.Fatalf("missing failure marker:\n%s", output)
	}
}

func TestVerifyModulesScriptFailsWhenAModuleIsMissing(t *testing.T) {
	repository, state := fakeModulesRepository(t)
	if err := os.RemoveAll(filepath.Join(repository, mcpModule)); err != nil {
		t.Fatal(err)
	}
	output, err := runVerifyModules(t, repository, state)
	if err == nil {
		t.Fatalf("verify-modules.sh must fail when a nested module is absent:\n%s", output)
	}
	if !strings.Contains(string(output), "nested module not found") {
		t.Fatalf("missing absent-module message:\n%s", output)
	}
}

func TestVerifyModulesScriptRequiresToolsInOfflineMode(t *testing.T) {
	repository, state := fakeModulesRepository(t)
	output, err := runVerifyModules(t, repository, state, "STATICCHECK_BIN=", "GOVULNCHECK_BIN=")
	if err == nil {
		t.Fatalf("offline mode without pinned tools must fail:\n%s", output)
	}
	if !strings.Contains(string(output), "is required in offline mode") {
		t.Fatalf("missing offline-tool message:\n%s", output)
	}
}

func fakeModulesRepository(t *testing.T) (string, string) {
	t.Helper()
	repository := t.TempDir()
	state := filepath.Join(repository, "state")
	for _, directory := range []string{
		filepath.Join(repository, "scripts"),
		filepath.Join(repository, "bin"),
		filepath.Join(repository, agentModule),
		filepath.Join(repository, mcpModule),
		state,
	} {
		if err := os.MkdirAll(directory, 0755); err != nil {
			t.Fatal(err)
		}
	}
	verifyScript, err := os.ReadFile("verify-modules.sh")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, "scripts", "verify-modules.sh"), verifyScript, 0755); err != nil {
		t.Fatal(err)
	}
	for _, module := range []string{agentModule, mcpModule} {
		goMod := "module example.com/" + strings.ReplaceAll(module, "/", "-") + "\n\ngo 1.26.6\n"
		if err := os.WriteFile(filepath.Join(repository, module, "go.mod"), []byte(goMod), 0644); err != nil {
			t.Fatal(err)
		}
	}
	fakeGo := `#!/usr/bin/env bash
set -euo pipefail
printf '%s\t%s\n' "${PWD:-}" "$*" >> "${MODULES_TEST_STATE}/go-calls"
if [[ -n "${MODULES_TEST_FAIL_MODULE:-}" && "${PWD:-}" == *"${MODULES_TEST_FAIL_MODULE}"* && "${1:-}" == "test" ]]; then
  exit 1
fi
exit 0
`
	fakeStaticcheck := `#!/usr/bin/env bash
printf '%s\n' "$*" >> "${MODULES_TEST_STATE}/staticcheck-calls"
exit 0
`
	fakeGovulncheck := `#!/usr/bin/env bash
printf '%s\n' "$*" >> "${MODULES_TEST_STATE}/govulncheck-calls"
exit 0
`
	for name, contents := range map[string]string{
		"go":          fakeGo,
		"staticcheck": fakeStaticcheck,
		"govulncheck": fakeGovulncheck,
	} {
		if err := os.WriteFile(filepath.Join(repository, "bin", name), []byte(contents), 0755); err != nil {
			t.Fatal(err)
		}
	}
	return repository, state
}

func runVerifyModules(t *testing.T, repository, state string, overrides ...string) ([]byte, error) {
	t.Helper()
	settings := map[string]string{
		"PATH":                        filepath.Join(repository, "bin") + string(os.PathListSeparator) + os.Getenv("PATH"),
		"RC_ALLOW_NETWORK":            "0",
		"STATICCHECK_BIN":             filepath.Join(repository, "bin", "staticcheck"),
		"STATICCHECK_EXPECTED_SHA256": fileSHA256(t, filepath.Join(repository, "bin", "staticcheck")),
		"GOVULNCHECK_BIN":             filepath.Join(repository, "bin", "govulncheck"),
		"GOVULNCHECK_EXPECTED_SHA256": fileSHA256(t, filepath.Join(repository, "bin", "govulncheck")),
		"GOCACHE":                     filepath.Join(repository, "gocache"),
		"GOMODCACHE":                  filepath.Join(repository, "gomodcache"),
		"MODULES_TEST_STATE":          state,
	}
	for _, override := range overrides {
		name, value, _ := strings.Cut(override, "=")
		settings[name] = value
	}
	applied := make([]string, 0, len(settings))
	for name, value := range settings {
		applied = append(applied, name+"="+value)
	}
	command := exec.Command("./scripts/verify-modules.sh")
	command.Dir = repository
	command.Env = releaseTestEnvironment(applied...)
	return command.CombinedOutput()
}

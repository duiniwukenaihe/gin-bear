//go:build !windows

package scripts

import (
	"errors"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// TestIntegrationScriptEnforcesRequiredEngines keeps CI honest: when an engine
// is listed in BEAR_INTEGRATION_REQUIRE, its absence must fail the run instead
// of degrading to a NOT_RUN skip that still exits 0.
func TestIntegrationScriptEnforcesRequiredEngines(t *testing.T) {
	content, err := os.ReadFile("test-integration.sh")
	if err != nil {
		t.Fatalf("read test-integration.sh: %v", err)
	}
	text := string(content)
	for _, want := range []string{
		"BEAR_INTEGRATION_REQUIRE",
		"unsupported engines",
		"demands engines that are unavailable",
		"exit 3",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("test-integration.sh missing required-engine enforcement %q:\n%s", want, text)
		}
	}
	if !strings.Contains(text, `if [ -n "${BEAR_INTEGRATION_REQUIRE:-}" ]; then`) {
		t.Fatalf("required-engine enforcement must be opt-in:\n%s", text)
	}
}

func TestIntegrationScriptFailsWhenRequiredEngineIsUnsupported(t *testing.T) {
	output, code := runIntegrationScript(t, "bogus")
	if code != 3 {
		t.Fatalf("exit code = %d, want 3 (output: %s)", code, output)
	}
	if !strings.Contains(output, "unsupported engines: bogus") {
		t.Fatalf("missing unsupported-engine message:\n%s", output)
	}
}

func TestIntegrationScriptFailsWhenRequiredMySQLIsAbsent(t *testing.T) {
	output, code := runIntegrationScript(t, "mysql")
	if code != 3 {
		t.Fatalf("exit code = %d, want 3 (output: %s)", code, output)
	}
	if !strings.Contains(output, "unavailable: mysql") {
		t.Fatalf("missing unavailable-mysql message:\n%s", output)
	}
}

func TestIntegrationScriptFailsWhenRequiredRedisIsUnreachable(t *testing.T) {
	output, code := runIntegrationScript(t, "redis")
	if code != 3 {
		t.Fatalf("exit code = %d, want 3 (output: %s)", code, output)
	}
	if !strings.Contains(output, "unavailable: redis") {
		t.Fatalf("missing unavailable-redis message:\n%s", output)
	}
}

// runIntegrationScript executes the real script far enough to reach the
// required-engine gate, without touching a live database or running go test.
// A dummy PG DSN suppresses the local disposable-role preparation, and Redis
// points at a closed loopback port so the probe fails immediately.
func runIntegrationScript(t *testing.T, required string) (string, int) {
	t.Helper()
	command := exec.Command("./test-integration.sh")
	command.Env = integrationTestEnvironment(
		"BEAR_INTEGRATION_REQUIRE="+required,
		"BEAR_INTEGRATION_PG_DSN=postgres://unused:unused@127.0.0.1:1/unused?sslmode=disable",
		"BEAR_INTEGRATION_REDIS_ADDR=127.0.0.1:"+closedLoopbackPort(t),
		"GOCACHE="+t.TempDir(),
		"GOMODCACHE="+t.TempDir(),
	)
	output, err := command.CombinedOutput()
	if err == nil {
		return string(output), 0
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("run test-integration.sh: %v\n%s", err, output)
	}
	return string(output), exitErr.ExitCode()
}

func integrationTestEnvironment(overrides ...string) []string {
	environment := make([]string, 0, len(os.Environ())+len(overrides))
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "BEAR_INTEGRATION_") {
			continue
		}
		environment = append(environment, entry)
	}
	return append(environment, overrides...)
}

func closedLoopbackPort(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return strconv.Itoa(port)
}

package integration

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve integration test path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
}

func runGo(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOSUMDB=sum.golang.org", "GOTOOLCHAIN=go1.26.6")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go %s failed: %v\n%s", strings.Join(args, " "), err, output)
	}
}

func buildRepoCLI(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "bear")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	runGo(t, repoRoot(t), "build", "-o", binary, "./cmd/bear")
	return binary
}

func runBinary(t *testing.T, dir, binary string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOSUMDB=sum.golang.org", "GOTOOLCHAIN=go1.26.6")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("run %s: %v", binary, err)
		}
		return stdout.String(), stderr.String(), exitErr.ExitCode()
	}
	return stdout.String(), stderr.String(), 0
}

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

func waitForLive(t *testing.T, client *http.Client, url string, output *bytes.Buffer) {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		response, err := client.Get(url) //nolint:gosec // loopback integration probe
		if err == nil {
			_, readErr := io.Copy(io.Discard, response.Body)
			closeErr := response.Body.Close()
			if readErr == nil && closeErr == nil && response.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("integration server never became live at %s\nserver output:\n%s", url, output.String())
}

func jsonRequest(t *testing.T, client *http.Client, method, url, body string, output *bytes.Buffer) map[string]any {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("%s %s: %v\nserver output:\n%s", method, url, err, output.String())
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		t.Fatalf("read %s %s: %v", method, url, err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode %s %s (%d): %v\n%s", method, url, response.StatusCode, err, payload)
	}
	return decoded
}

// writePGAppConfig points the freshly scaffolded project at the disposable
// PostgreSQL database. Credentials come from the test DSN; nothing here is
// committed or reused outside this run.
func writePGAppConfig(t *testing.T, project, dsn string) {
	t.Helper()
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse PG DSN: %v", err)
	}
	password, _ := parsed.User.Password()
	port := parsed.Port()
	if port == "" {
		port = "5432"
	}
	config := fmt.Sprintf(`server:
  port: 8080
  name: pg-app
database:
  enabled: true
  type: "postgres"
  host: %q
  port: %q
  user: %q
  password: %q
  dbname: %q
config:
  framework.response_mode: "envelope"
`,
		parsed.Hostname(), port, parsed.User.Username(), password, strings.TrimPrefix(parsed.Path, "/"))
	path := filepath.Join(project, "application.yaml")
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
}

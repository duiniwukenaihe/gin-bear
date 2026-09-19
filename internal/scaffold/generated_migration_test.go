package scaffold

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestGeneratedAPIResourceBootsAfterApplyingGeneratedMigration covers the
// combination that previously had no coverage at all: `bear new` followed by
// `bear gen api`.
//
// The generated repository injects *bear.GormAdapter, which only exists while
// the database is enabled, and reads a table that only the generated migration
// creates. A project that skips either step fails at startup or on the first
// request, and no test used to notice.
func TestGeneratedAPIResourceBootsAfterApplyingGeneratedMigration(t *testing.T) {
	project := filepath.Join(t.TempDir(), "golden-path")
	if err := Generate(context.Background(), Options{
		Name:             "golden-path",
		Module:           "example.com/golden-path",
		Directory:        project,
		FrameworkVersion: "v0.0.0",
		FrameworkReplace: repoRoot(t),
	}); err != nil {
		t.Fatal(err)
	}
	// Enabling a database is the documented step between `bear new` and
	// `bear gen api`; the scaffold keeps it disabled so the default project also
	// starts under GIN_MODE=release.
	enableGeneratedDatabase(t, project)

	bearBinary := buildCLI(t, "./cmd/bear", "bear")
	stdout, stderr, code := runCommand(t, project, bearBinary, "gen", "api", "invoice", "--fields", "name:string,email:email")
	if code != 0 {
		t.Fatalf("resource generation failed (%d):\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}

	upSQL := readFile(t, filepath.Join(project, "migrations", "001_create_invoice.up.sql"))
	if !strings.Contains(upSQL, `CREATE TABLE "invoice"`) {
		t.Fatalf("generated migration does not create the invoice table:\n%s", upSQL)
	}
	downSQL := readFile(t, filepath.Join(project, "migrations", "001_create_invoice.down.sql"))
	if !strings.Contains(downSQL, `DROP TABLE IF EXISTS "invoice"`) {
		t.Fatalf("generated migration does not drop the invoice table:\n%s", downSQL)
	}

	runGo(t, project, "mod", "tidy")
	// Schema changes are a deploy step, never part of server startup.
	runGo(t, project, "run", "./cmd/migrate")

	assertGeneratedCRUDRoundTrip(t, project)
}

// enableGeneratedDatabase turns the scaffold's disabled database block into the
// local SQLite database the generated resource needs. It replaces the whole
// block rather than a bare "enabled: false" because the template also disables
// gRPC earlier in the file.
func enableGeneratedDatabase(t *testing.T, project string) {
	t.Helper()
	path := filepath.Join(project, "application.yaml")
	config := readFile(t, path)
	enabled := strings.Replace(config, "database:\n  enabled: false",
		"database:\n  enabled: true\n  type: \"sqlite\"\n  dsn: \"golden-path.db\"", 1)
	if enabled == config {
		t.Fatalf("generated application.yaml has no disabled database block to enable:\n%s", config)
	}
	if err := os.WriteFile(path, []byte(enabled), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A fresh project must start under GIN_MODE=release, which the framework treats
// as production. Enabling the database by default would make it reject SQLite as
// a production database, so the scaffold default has to stay disabled.
func TestGeneratedConfigurationKeepsDatabaseDisabledByDefault(t *testing.T) {
	project := filepath.Join(t.TempDir(), "release-clean")
	if err := Generate(context.Background(), Options{
		Name:             "release-clean",
		Module:           "example.com/release-clean",
		Directory:        project,
		FrameworkVersion: "v0.0.0",
		FrameworkReplace: repoRoot(t),
	}); err != nil {
		t.Fatal(err)
	}
	config := readFile(t, filepath.Join(project, "application.yaml"))
	if !strings.Contains(config, "database:\n  enabled: false") {
		t.Fatalf("scaffold default no longer disables the database:\n%s", config)
	}
}

// assertGeneratedCRUDRoundTrip boots the generated server against the migrated
// database and exercises one create/read round trip.
func assertGeneratedCRUDRoundTrip(t *testing.T, project string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}

	serverBinary := filepath.Join(t.TempDir(), "generated-server")
	if runtime.GOOS == "windows" {
		serverBinary += ".exe"
	}
	runGo(t, project, "build", "-o", serverBinary, "./cmd/server")

	var output bytes.Buffer
	cmd := exec.Command(serverBinary)
	cmd.Dir = project
	cmd.Env = append(os.Environ(), fmt.Sprintf("BEAR_SERVER_PORT=%d", port), "GOSUMDB=sum.golang.org", "GOTOOLCHAIN=go1.25.14")
	cmd.Stdout = &output
	cmd.Stderr = &output
	prepareGeneratedProcess(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	client := &http.Client{Timeout: 5 * time.Second}
	waitForGeneratedLiveness(t, client, base+"/live", &output)

	created := generatedJSONRequest(t, client, http.MethodPost, base+"/api/v1/invoice",
		`{"name":"first","email":"a@example.com"}`, &output)
	if created["code"] != float64(http.StatusCreated) {
		t.Fatalf("POST /api/v1/invoice = %v\nserver output:\n%s", created, output.String())
	}
	data, ok := created["data"].(map[string]any)
	if !ok || data["id"] == nil {
		t.Fatalf("POST /api/v1/invoice returned no id: %v", created)
	}

	list := generatedJSONRequest(t, client, http.MethodGet, base+"/api/v1/invoice", "", &output)
	payload, ok := list["data"].(map[string]any)
	if !ok {
		t.Fatalf("GET /api/v1/invoice returned no data: %v", list)
	}
	if total, _ := payload["total"].(float64); total != 1 {
		t.Fatalf("GET /api/v1/invoice total = %v, want 1\nserver output:\n%s", payload["total"], output.String())
	}
}

func waitForGeneratedLiveness(t *testing.T, client *http.Client, url string, output *bytes.Buffer) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		response, err := client.Get(url) //nolint:gosec // loopback smoke test
		if err == nil {
			_, readErr := io.Copy(io.Discard, response.Body)
			closeErr := response.Body.Close()
			if readErr == nil && closeErr == nil && response.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("generated server never became live at %s\nserver output:\n%s", url, output.String())
}

func generatedJSONRequest(t *testing.T, client *http.Client, method, url, body string, output *bytes.Buffer) map[string]any {
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

// TestGeneratedAPIWithDisabledDatabaseWarnsWithoutBlocking pins the
// generation-time contract: a project that disables the database still gets its
// resource, because the application may register *bear.GormAdapter itself, but
// the operator is told that nothing will provide the bean automatically and no
// migration is written for a dialect the project has not chosen.
func TestGeneratedAPIWithDisabledDatabaseWarnsWithoutBlocking(t *testing.T) {
	project := filepath.Join(t.TempDir(), "disabled-database")
	if err := Generate(context.Background(), Options{
		Name:             "disabled-database",
		Module:           "example.com/disabled-database",
		Directory:        project,
		FrameworkVersion: "v0.0.0",
		FrameworkReplace: repoRoot(t),
	}); err != nil {
		t.Fatal(err)
	}

	bearBinary := buildCLI(t, "./cmd/bear", "bear")
	stdout, stderr, code := runCommand(t, project, bearBinary, "gen", "api", "invoice")
	if code != 0 {
		t.Fatalf("resource generation failed with the database disabled (%d):\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "Warning:") || !strings.Contains(stderr, "database.enabled") {
		t.Fatalf("stderr does not warn about database.enabled:\nstderr:\n%s", stderr)
	}
	if strings.Contains(stdout, "Warning:") {
		t.Fatalf("warning leaked onto stdout:\nstdout:\n%s", stdout)
	}
	if _, err := os.Stat(filepath.Join(project, "internal", "invoice")); err != nil {
		t.Fatalf("warned generation did not publish the resource package: %v", err)
	}
	if _, err := os.Stat(filepath.Join(project, "migrations")); !os.IsNotExist(err) {
		t.Fatalf("generation without an enabled database still published migrations: %v", err)
	}
}

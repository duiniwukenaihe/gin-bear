package scaffold

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

func listGeneratedFiles(t *testing.T, dir string) []string {
	t.Helper()
	var files []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	return files
}

// TestMinimalProfileSnapshotUnchanged pins the default scaffold output: the
// production profile must never leak files into the minimal default.
func TestMinimalProfileSnapshotUnchanged(t *testing.T) {
	for _, profile := range []string{"", "minimal"} {
		project := filepath.Join(t.TempDir(), "minimal-snapshot")
		if err := Generate(context.Background(), Options{
			Name:             "minimal-snapshot",
			Module:           "example.com/minimal-snapshot",
			Directory:        project,
			FrameworkVersion: "v0.0.0",
			FrameworkReplace: repoRoot(t),
			Profile:          profile,
		}); err != nil {
			t.Fatal(err)
		}
		got := listGeneratedFiles(t, project)
		want := []string{
			".bear/scaffold.json",
			".gitignore",
			"application-prod.yaml.example",
			"application.yaml",
			"cmd/migrate/main.go",
			"cmd/server/main.go",
			"cmd/server/signals_unix.go",
			"cmd/server/signals_windows.go",
			"go.mod",
			"internal/app/app.go",
			"internal/app/modules_gen.go",
			"internal/app/routes.go",
		}
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Fatalf("minimal profile files changed (profile %q):\ngot:\n%s\nwant:\n%s", profile, strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
	}
}

func TestInvalidProfileIsRejected(t *testing.T) {
	project := filepath.Join(t.TempDir(), "bad-profile")
	err := Generate(context.Background(), Options{
		Name:             "bad-profile",
		Module:           "example.com/bad-profile",
		Directory:        project,
		FrameworkVersion: "v0.0.0",
		FrameworkReplace: repoRoot(t),
		Profile:          "enterprise",
	})
	if err == nil || !strings.Contains(err.Error(), "profile") {
		t.Fatalf("Generate with bad profile = %v, want profile error", err)
	}
	if _, statErr := os.Stat(project); !os.IsNotExist(statErr) {
		t.Fatalf("rejected profile still created %s", project)
	}
}

// TestProductionProfileContents verifies the deployment assets render,
// including dot-paths (which once silently failed to embed), and that no
// production-usable secret is committed anywhere except the dev-labeled compose.
func TestProductionProfileContents(t *testing.T) {
	project := filepath.Join(t.TempDir(), "production-contents")
	if err := Generate(context.Background(), Options{
		Name:             "production-contents",
		Module:           "example.com/production-contents",
		Directory:        project,
		FrameworkVersion: "v0.0.0",
		FrameworkReplace: repoRoot(t),
		Profile:          "production",
	}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"README.md",
		"Makefile",
		"Dockerfile",
		".dockerignore",
		".github/workflows/ci.yml",
		"compose.dev.yaml",
		"application.yaml",
		"cmd/server/main.go",
	} {
		if _, err := os.Stat(filepath.Join(project, filepath.FromSlash(want))); err != nil {
			t.Fatalf("production profile missing %s: %v", want, err)
		}
	}

	dockerfile := readFile(t, filepath.Join(project, "Dockerfile"))
	for _, want := range []string{
		"golang:1.26.6-bookworm",
		"distroless/static-debian12",
		"ENTRYPOINT",
		"./cmd/server",
		"./cmd/migrate",
		"/app/migrate",
	} {
		if !strings.Contains(dockerfile, want) {
			t.Fatalf("Dockerfile missing %q:\n%s", want, dockerfile)
		}
	}
	if !strings.Contains(dockerfile, "USER") && !strings.Contains(dockerfile, "nonroot") {
		t.Fatalf("Dockerfile does not run non-root:\n%s", dockerfile)
	}
	if strings.Count(dockerfile, "FROM ") != 2 {
		t.Fatalf("Dockerfile is not multi-stage:\n%s", dockerfile)
	}

	readme := readFile(t, filepath.Join(project, "README.md"))
	for _, want := range []string{
		"Configuration priority",
		"Secret injection",
		"separate deploy step",
		"Graceful shutdown",
		"Backup and restore",
		"Upgrades",
		"make build",
		"./bin/migrate",
		"./bin/server",
	} {
		if !strings.Contains(readme, want) {
			t.Fatalf("README missing %q:\n%s", want, readme)
		}
	}
	if strings.Contains(readme, "make docker`") {
		t.Fatalf("README references the non-existent `make docker` target:\n%s", readme)
	}

	makefile := readFile(t, filepath.Join(project, "Makefile"))
	for _, want := range []string{"test:", "build:", "migrate:", "docker-build:"} {
		if !strings.Contains(makefile, want) {
			t.Fatalf("Makefile missing target %q", want)
		}
	}

	compose := readFile(t, filepath.Join(project, "compose.dev.yaml"))
	if !strings.Contains(compose, "DEVELOPMENT AND ACCEPTANCE ONLY") {
		t.Fatal("compose.dev.yaml does not label itself dev/acceptance-only")
	}

	// Secret hygiene: only the dev-labeled compose may carry a literal
	// credential; everything else must use placeholders or env references.
	for _, name := range []string{"README.md", "Dockerfile", "Makefile", ".github/workflows/ci.yml", "application.yaml"} {
		contents := readFile(t, filepath.Join(project, filepath.FromSlash(name)))
		if strings.Contains(contents, "dev-only-password") {
			t.Fatalf("%s carries a literal credential", name)
		}
	}
}

// productionPGDSN returns a disposable PostgreSQL DSN for the production
// acceptance run, or skips when no server is available.
func productionPGDSN(t *testing.T) (dsn string, cleanup func()) {
	t.Helper()
	if dsn := os.Getenv("BEAR_PRODUCTION_PG_DSN"); dsn != "" {
		return dsn, func() {}
	}
	if dsn := os.Getenv("BEAR_INTEGRATION_PG_DSN"); dsn != "" {
		return dsn, func() {}
	}
	if _, err := exec.LookPath("pg_isready"); err != nil {
		t.Skip("NOT_RUN: no postgres for production acceptance (set BEAR_INTEGRATION_PG_DSN)")
	}
	if out, err := exec.Command("pg_isready", "-q", "-h", "/tmp", "-p", "5432").CombinedOutput(); err != nil {
		t.Skipf("NOT_RUN: local postgres not ready: %s", out)
	}
	name := "bear_w2_test"
	statements := []string{
		"DROP DATABASE IF EXISTS " + name + ";",
		"CREATE DATABASE " + name + ";",
	}
	for _, stmt := range statements {
		if out, err := exec.Command("psql", "-h", "/tmp", "-U", "zhangpeng", "-d", "postgres", "-c", stmt).CombinedOutput(); err != nil {
			t.Fatalf("prepare disposable db: %v\n%s", err, out)
		}
	}
	return "postgres://zhangpeng@127.0.0.1:5432/" + name + "?sslmode=disable", func() {
		_, _ = exec.Command("psql", "-h", "/tmp", "-U", "zhangpeng", "-d", "postgres", "-c", "DROP DATABASE IF EXISTS "+name+";").CombinedOutput()
	}
}

// TestProductionProfileBuildsBootsAndExits is the W2 acceptance: a production
// project builds from zero, migrates, serves CRUD against a real database,
// and exits cleanly on TERM.
func TestProductionProfileBuildsBootsAndExits(t *testing.T) {
	dsn, cleanup := productionPGDSN(t)
	defer cleanup()

	project := filepath.Join(t.TempDir(), "production-boot")
	if err := Generate(context.Background(), Options{
		Name:             "production-boot",
		Module:           "example.com/production-boot",
		Directory:        project,
		FrameworkVersion: "v0.0.0",
		FrameworkReplace: repoRoot(t),
		Profile:          "production",
	}); err != nil {
		t.Fatal(err)
	}
	writeProductionTestConfig(t, project, dsn)

	bearBinary := buildCLI(t, "./cmd/bear", "bear")
	stdout, stderr, code := runCommand(t, project, bearBinary, "gen", "api", "widget", "--fields", "name:string")
	if code != 0 {
		t.Fatalf("gen api failed (%d):\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	runGo(t, project, "mod", "tidy")
	runGo(t, project, "test", "./...")
	runGo(t, project, "build", "-o", filepath.Join(project, "bin", "migrate"), "./cmd/migrate")
	t.Setenv("BEAR_ENV", "prod")
	t.Setenv("GIN_MODE", "release")
	if stdout, stderr, code := runCommand(t, project, filepath.Join(project, "bin", "migrate")); code != 0 {
		t.Fatalf("native migrate failed (%d):\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	runGo(t, project, "build", "-o", filepath.Join(project, "bin", "server"), "./cmd/server")

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	portText := strconv.Itoa(port)
	serverBinary := filepath.Join(project, "bin", "server")
	var output bytes.Buffer
	cmd := exec.Command(serverBinary)
	cmd.Dir = project
	cmd.Env = append(os.Environ(), "BEAR_SERVER_PORT="+portText)
	cmd.Stdout = &output
	cmd.Stderr = &output
	prepareGeneratedProcess(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	base := "http://127.0.0.1:" + portText
	client := &http.Client{Timeout: 10 * time.Second}
	waitForGeneratedLiveness(t, client, base+"/live", &output)
	created := generatedJSONRequest(t, client, http.MethodPost, base+"/api/v1/widget", `{"name":"prod-first"}`, &output)
	if created["code"] != float64(http.StatusCreated) {
		t.Fatalf("POST widget = %v\nserver output:\n%s", created, output.String())
	}
	list := generatedJSONRequest(t, client, http.MethodGet, base+"/api/v1/widget", "", &output)
	payload, ok := list["data"].(map[string]any)
	if !ok || payload["total"] != float64(1) {
		t.Fatalf("GET widget = %v, want total 1\nserver output:\n%s", list, output.String())
	}

	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("signal server: %v", err)
	}
	go func() {
		_, _ = cmd.Process.Wait()
		close(exited)
	}()
	select {
	case <-exited:
	case <-time.After(20 * time.Second):
		t.Fatalf("production server did not exit after interrupt\nserver output:\n%s", output.String())
	}
}

// writeProductionTestConfig points a generated project at a test PostgreSQL
// database. It mirrors the scaffold default (envelope responses) so the
// acceptance run exercises the documented configuration shape.
func writeProductionTestConfig(t *testing.T, project, dsn string) {
	t.Helper()
	config := "server:\n  port: 8080\n  name: production-boot\ndatabase:\n  enabled: true\n  type: \"postgres\"\n" +
		"  dsn: " + strconv.Quote(dsn) + "\nconfig:\n  framework.strict: true\n  framework.allow_compatibility_in_production: false\n  framework.response_mode: \"envelope\"\n"
	if err := os.WriteFile(filepath.Join(project, "application.yaml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
}

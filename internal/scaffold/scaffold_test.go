package scaffold

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"go/format"
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

	"gopkg.in/yaml.v2"
)

func TestGenerateProjectBuildsTestsAndServesHealth(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "billing-api")
	err := Generate(context.Background(), Options{
		Name:             "billing-api",
		Module:           "example.com/billing-api",
		Directory:        dir,
		FrameworkVersion: "v0.10.0-rc.1",
	})
	if err != nil {
		t.Fatal(err)
	}

	goMod, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"module example.com/billing-api",
		"require github.com/duiniwukenaihe/gin-bear v0.10.0-rc.1",
	} {
		if !strings.Contains(string(goMod), want) {
			t.Fatalf("generated go.mod missing %q:\n%s", want, goMod)
		}
	}
	for _, forbidden := range []string{"pkg/bear", ".git", ".github", "docs", "README.md", "SECURITY.md"} {
		if _, err := os.Stat(filepath.Join(dir, forbidden)); !os.IsNotExist(err) {
			t.Fatalf("generated project must not contain repository asset %q", forbidden)
		}
	}

	runGo(t, dir, "mod", "edit", "-replace", "github.com/duiniwukenaihe/gin-bear="+repoRoot(t))
	runGo(t, dir, "mod", "tidy")
	runGo(t, dir, "test", "./...", "-count=1")
	runGeneratedServerHealthCheck(t, dir, "/live")
}

func TestGenerateRejectsInvalidOptionsWithoutPublishingFiles(t *testing.T) {
	tests := []struct {
		name string
		opts Options
	}{
		{name: "name", opts: Options{Module: "example.com/app", Directory: filepath.Join(t.TempDir(), "app"), FrameworkVersion: "v1.2.3"}},
		{name: "module", opts: Options{Name: "app", Directory: filepath.Join(t.TempDir(), "app"), FrameworkVersion: "v1.2.3"}},
		{name: "directory", opts: Options{Name: "app", Module: "example.com/app", FrameworkVersion: "v1.2.3"}},
		{name: "version", opts: Options{Name: "app", Module: "example.com/app", Directory: filepath.Join(t.TempDir(), "app")}},
		{name: "unsafe module", opts: Options{Name: "app", Module: "example.com/app\nreplace bad => .", Directory: filepath.Join(t.TempDir(), "app"), FrameworkVersion: "v1.2.3"}},
		{name: "invalid module segment", opts: Options{Name: "app", Module: "example.com/../app", Directory: filepath.Join(t.TempDir(), "app"), FrameworkVersion: "v1.2.3"}},
		{name: "invalid semantic version", opts: Options{Name: "app", Module: "example.com/app", Directory: filepath.Join(t.TempDir(), "app"), FrameworkVersion: "vbanana"}},
		{name: "Go-incompatible semantic version", opts: Options{Name: "app", Module: "example.com/app", Directory: filepath.Join(t.TempDir(), "app"), FrameworkVersion: "v1.2.3-01"}},
		{name: "Unicode module path", opts: Options{Name: "app", Module: "example.com/应用", Directory: filepath.Join(t.TempDir(), "app"), FrameworkVersion: "v1.2.3"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := Generate(context.Background(), tt.opts); err == nil {
				t.Fatal("expected validation error")
			}
			if tt.opts.Directory != "" {
				if _, err := os.Stat(tt.opts.Directory); !os.IsNotExist(err) {
					t.Fatalf("invalid generation published destination: %v", err)
				}
			}
		})
	}
}

func TestGenerateQuotesNamesInValidYAML(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "app")
	name := `billing "critical": api`
	if err := Generate(context.Background(), Options{
		Name:             name,
		Module:           "example.com/app",
		Directory:        destination,
		FrameworkVersion: "v1.2.3",
	}); err != nil {
		t.Fatal(err)
	}

	for _, filename := range []string{"application.yaml", "application-prod.yaml.example"} {
		contents, err := os.ReadFile(filepath.Join(destination, filename))
		if err != nil {
			t.Fatal(err)
		}
		var config struct {
			Server struct {
				Name string `yaml:"name"`
			} `yaml:"server"`
			Tracing struct {
				ServiceName string `yaml:"service_name"`
			} `yaml:"tracing"`
		}
		if err := yaml.Unmarshal(contents, &config); err != nil {
			t.Fatalf("parse generated %s: %v\n%s", filename, err, contents)
		}
		if config.Server.Name != name || config.Tracing.ServiceName != name {
			t.Fatalf("%s names = server %q tracing %q, want %q", filename, config.Server.Name, config.Tracing.ServiceName, name)
		}
	}
}

func TestGenerateIsAtomicAndHonorsCancellation(t *testing.T) {
	parent := t.TempDir()
	destination := filepath.Join(parent, "app")
	if err := os.Mkdir(destination, 0755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(destination, "keep.txt")
	if err := os.WriteFile(marker, []byte("keep"), 0644); err != nil {
		t.Fatal(err)
	}

	opts := Options{Name: "app", Module: "example.com/app", Directory: destination, FrameworkVersion: "v1.2.3"}
	if err := Generate(context.Background(), opts); err == nil {
		t.Fatal("expected existing destination error")
	}
	if got, err := os.ReadFile(marker); err != nil || string(got) != "keep" {
		t.Fatalf("existing destination changed: content=%q err=%v", got, err)
	}

	cancelledDestination := filepath.Join(parent, "cancelled")
	opts.Directory = cancelledDestination
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Generate(ctx, opts); err == nil {
		t.Fatal("expected cancellation error")
	}
	if _, err := os.Stat(cancelledDestination); !os.IsNotExist(err) {
		t.Fatalf("cancelled generation published destination: %v", err)
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".cancelled.tmp-") {
			t.Fatalf("cancelled generation leaked temporary directory %q", entry.Name())
		}
	}
}

func TestBothExecutablePathsShareHelpAndErrors(t *testing.T) {
	bearBinary := buildCLI(t, "./cmd/bear", "bear")
	legacyBinary := buildCLI(t, "./cmd/bear-cli", "bear-cli")

	bearHelp, bearHelpErr, bearHelpCode := runCommand(t, repoRoot(t), bearBinary, "--help")
	legacyHelp, legacyHelpErr, legacyHelpCode := runCommand(t, repoRoot(t), legacyBinary, "--help")
	if bearHelpCode != 0 || legacyHelpCode != 0 {
		t.Fatalf("help exit codes differ: bear=%d legacy=%d", bearHelpCode, legacyHelpCode)
	}
	if bearHelp != legacyHelp || bearHelpErr != legacyHelpErr {
		t.Fatalf("executable help differs:\nbear stdout:\n%s\nbear stderr:\n%s\nlegacy stdout:\n%s\nlegacy stderr:\n%s", bearHelp, bearHelpErr, legacyHelp, legacyHelpErr)
	}
	for _, want := range []string{"new", "gen", "--help"} {
		if !strings.Contains(bearHelp, want) {
			t.Fatalf("help missing %q:\n%s", want, bearHelp)
		}
	}

	bearOut, bearErr, bearCode := runCommand(t, repoRoot(t), bearBinary, "gen", "unsupported", "widget")
	legacyOut, legacyErr, legacyCode := runCommand(t, repoRoot(t), legacyBinary, "gen", "unsupported", "widget")
	if bearCode == 0 || legacyCode == 0 {
		t.Fatalf("invalid command succeeded: bear=%d legacy=%d", bearCode, legacyCode)
	}
	if bearOut != legacyOut || bearErr != legacyErr || bearCode != legacyCode {
		t.Fatalf("executable errors differ:\nbear (%d) stdout:\n%s\nstderr:\n%s\nlegacy (%d) stdout:\n%s\nstderr:\n%s", bearCode, bearOut, bearErr, legacyCode, legacyOut, legacyErr)
	}
}

func TestResourceGenerationUsesInternalPackagesDecimalAndAtomicWrites(t *testing.T) {
	project := filepath.Join(t.TempDir(), "orders-api")
	if err := Generate(context.Background(), Options{
		Name:             "orders-api",
		Module:           "example.com/orders-api",
		Directory:        project,
		FrameworkVersion: "v0.10.0-rc.1",
	}); err != nil {
		t.Fatal(err)
	}
	bearBinary := buildCLI(t, "./cmd/bear", "bear")
	stdout, stderr, code := runCommand(t, project, bearBinary, "gen", "api", "invoice", "--fields", "amount:decimal,published_at:datetime")
	if code != 0 {
		t.Fatalf("resource generation failed (%d):\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}

	resourceDir := filepath.Join(project, "internal", "invoice")
	entries, err := os.ReadDir(resourceDir)
	if err != nil {
		t.Fatal(err)
	}
	wantFiles := map[string]bool{
		"controller.go":   false,
		"dto.go":          false,
		"model.go":        false,
		"module.go":       false,
		"repository.go":   false,
		"router.go":       false,
		"service.go":      false,
		"service_test.go": false,
	}
	for _, entry := range entries {
		if _, ok := wantFiles[entry.Name()]; !ok {
			t.Fatalf("unexpected generated file %q", entry.Name())
		}
		wantFiles[entry.Name()] = true
		if strings.HasSuffix(entry.Name(), ".go") {
			path := filepath.Join(resourceDir, entry.Name())
			contents, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			formatted, formatErr := format.Source(contents)
			if formatErr != nil {
				t.Fatalf("generated Go file %s is invalid: %v", entry.Name(), formatErr)
			}
			if !bytes.Equal(contents, formatted) {
				t.Fatalf("generated Go file %s is not gofmt formatted", entry.Name())
			}
		}
	}
	for filename, present := range wantFiles {
		if !present {
			t.Fatalf("missing generated file %q", filename)
		}
	}
	model := readFile(t, filepath.Join(resourceDir, "model.go"))
	dto := readFile(t, filepath.Join(resourceDir, "dto.go"))
	for filename, contents := range map[string]string{"model.go": model, "dto.go": dto} {
		if !strings.Contains(contents, `"github.com/shopspring/decimal"`) || !strings.Contains(contents, "decimal.Decimal") {
			t.Fatalf("%s does not use decimal.Decimal:\n%s", filename, contents)
		}
	}
	if _, err := os.Stat(filepath.Join(project, "pkg", "invoice")); !os.IsNotExist(err) {
		t.Fatalf("resource was generated under pkg: %v", err)
	}

	runGo(t, project, "mod", "edit", "-replace", "github.com/duiniwukenaihe/gin-bear="+repoRoot(t))
	runGo(t, project, "mod", "tidy")
	runGo(t, project, "test", "./...", "-count=1")

	failedDir := filepath.Join(project, "internal", "broken")
	_, _, code = runCommand(t, project, bearBinary, "gen", "api", "broken", "--fields", "missing-type")
	if code == 0 {
		t.Fatal("invalid field definition unexpectedly succeeded")
	}
	if _, err := os.Stat(failedDir); !os.IsNotExist(err) {
		t.Fatalf("failed generation left a partial package: %v", err)
	}

	marker := filepath.Join(resourceDir, "keep.txt")
	if err := os.WriteFile(marker, []byte("keep"), 0644); err != nil {
		t.Fatal(err)
	}
	_, _, code = runCommand(t, project, bearBinary, "gen", "api", "invoice")
	if code == 0 {
		t.Fatal("generation over an existing package unexpectedly succeeded")
	}
	if got := readFile(t, marker); got != "keep" {
		t.Fatalf("existing package was modified: %q", got)
	}
}

func runGo(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOSUMDB=sum.golang.org", "GOTOOLCHAIN=go1.25.12")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go %s failed: %v\n%s", strings.Join(args, " "), err, output)
	}
}

func buildCLI(t *testing.T, packagePath, name string) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), name)
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	runGo(t, repoRoot(t), "build", "-o", binary, packagePath)
	return binary
}

func runCommand(t *testing.T, dir, binary string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOSUMDB=sum.golang.org", "GOTOOLCHAIN=go1.25.12")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return stdout.String(), stderr.String(), 0
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("run %s: %v", binary, err)
	}
	return stdout.String(), stderr.String(), exitErr.ExitCode()
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}

func runGeneratedServerHealthCheck(t *testing.T, dir, path string) {
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
	runGo(t, dir, "build", "-o", serverBinary, "./cmd/server")

	cmd := exec.Command(serverBinary)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), fmt.Sprintf("BEAR_SERVER_PORT=%d", port), "GOSUMDB=sum.golang.org", "GOTOOLCHAIN=go1.25.12")
	prepareGeneratedProcess(cmd)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	stderrDone := make(chan []byte, 1)
	stdoutDone := make(chan []byte, 1)
	go func() {
		output, _ := io.ReadAll(stderr)
		stderrDone <- output
	}()
	go func() {
		output, _ := io.ReadAll(stdout)
		stdoutDone <- output
	}()
	stopped := false
	t.Cleanup(func() {
		if !stopped && cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})

	url := fmt.Sprintf("http://127.0.0.1:%d%s", port, path)
	deadline := time.Now().Add(30 * time.Second)
	for {
		resp, requestErr := http.Get(url) //nolint:gosec // loopback smoke test
		if requestErr == nil {
			body, readErr := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if readErr != nil {
				t.Fatal(readErr)
			}
			if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"status":"ok"`) {
				t.Fatalf("GET %s = %d %s", path, resp.StatusCode, body)
			}
			break
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			stopped = true
			t.Fatalf("generated server did not become healthy: %v\n%s\n%s", requestErr, <-stdoutDone, <-stderrDone)
		}
		time.Sleep(100 * time.Millisecond)
	}

	if err := cancelGeneratedProcess(cmd.Process.Pid); err != nil {
		t.Fatal(err)
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()
	var waitErr error
	select {
	case waitErr = <-waitDone:
		stopped = true
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		<-waitDone
		stopped = true
		t.Fatalf("generated server did not exit after cancellation\n%s\n%s", <-stdoutDone, <-stderrDone)
	}
	serverOutput := string(<-stdoutDone) + string(<-stderrDone)
	if waitErr != nil {
		t.Fatalf("generated server exited with an error after cancellation: %v\n%s", waitErr, serverOutput)
	}
	if !strings.Contains(serverOutput, "WhiteBear returning to ice") {
		t.Fatalf("generated server skipped graceful cancellation:\n%s", serverOutput)
	}
	closeDeadline := time.Now().Add(3 * time.Second)
	for {
		resp, requestErr := http.Get(url) //nolint:gosec // loopback smoke test
		if requestErr != nil {
			break
		}
		_ = resp.Body.Close()
		if time.Now().After(closeDeadline) {
			t.Fatalf("generated server still answers after cancellation: %s", url)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve scaffold test path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
}

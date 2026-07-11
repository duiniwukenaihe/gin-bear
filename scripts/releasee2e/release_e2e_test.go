//go:build !windows

package releasee2e

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/duiniwukenaihe/gin-bear/internal/cli"
	"github.com/duiniwukenaihe/gin-bear/internal/scaffold"
)

const releaseSecret = "task10-release-secret-7Yp3mQ9"

func TestReleaseCandidateApplications(t *testing.T) {
	if os.Getenv("BEAR_RELEASE_E2E") != "1" {
		t.Skip("release-only application E2E")
	}
	repository := repositoryRoot(t)

	t.Run("legacy-v0.9-style", func(t *testing.T) {
		directory := createLegacyFixture(t, repository)
		binary := buildFixture(t, directory, ".")
		exerciseApplication(t, binary, directory)
	})

	t.Run("newly-generated", func(t *testing.T) {
		directory := createGeneratedFixture(t, repository)
		binary := buildFixture(t, directory, "./cmd/server")
		exerciseApplication(t, binary, directory)
	})
}

func createLegacyFixture(t *testing.T, repository string) string {
	t.Helper()
	directory := t.TempDir()
	writeFile(t, filepath.Join(directory, "go.mod"), fmt.Sprintf(`module example.com/legacy-release-check

go 1.25.0

require github.com/duiniwukenaihe/gin-bear v0.10.0-rc.1

replace github.com/duiniwukenaihe/gin-bear => %s
`, repository))
	writeFile(t, filepath.Join(directory, "main.go"), legacyFixtureSource)
	runGo(t, directory, "mod", "tidy")
	return directory
}

func createGeneratedFixture(t *testing.T, repository string) string {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "generated-release-check")
	if err := scaffold.Generate(context.Background(), scaffold.Options{
		Name:             "generated-release-check",
		Module:           "example.com/generated-release-check",
		Directory:        directory,
		FrameworkVersion: "v0.10.0-rc.1",
	}); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(directory, "internal", "app", "app.go"), generatedFixtureAppSource)
	runGo(t, directory, "mod", "edit", "-replace", "github.com/duiniwukenaihe/gin-bear="+repository)
	runGo(t, directory, "mod", "tidy")
	generateFixtureResource(t, directory)
	runGo(t, directory, "mod", "tidy")
	runGo(t, directory, "test", "./...", "-count=1")
	return directory
}

func generateFixtureResource(t *testing.T, directory string) {
	t.Helper()
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(directory); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(workingDirectory); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	}()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := cli.Execute([]string{"gen", "api", "invoice", "--fields", "amount:decimal,published_at:datetime"}, &stdout, &stderr); code != 0 {
		t.Fatalf("generate resource exit code = %d\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}
}

func buildFixture(t *testing.T, directory, packagePath string) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "release-app")
	runGo(t, directory, "build", "-o", binary, packagePath)
	return binary
}

func exerciseApplication(t *testing.T, binary, directory string) {
	t.Helper()
	port := reservePort(t)
	var stdout lockedBuffer
	var stderr lockedBuffer
	command := exec.Command(binary)
	command.Dir = directory
	command.Env = commandEnvironment(map[string]string{
		"BEAR_E2E_PORT": strconv.Itoa(port),
		"BEAR_ENV":      "test",
		"GIN_MODE":      "release",
	})
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- command.Wait() }()
	stopped := false
	defer func() {
		if stopped {
			return
		}
		_ = command.Process.Kill()
		select {
		case <-waitDone:
		case <-time.After(5 * time.Second):
		}
	}()

	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	client := &http.Client{Timeout: 2 * time.Second}
	if err := waitForLive(client, baseURL+"/live", 20*time.Second); err != nil {
		t.Fatalf("startup: %v", err)
	}
	assertResponse(t, client, http.MethodGet, baseURL+"/live", "", nil, http.StatusOK, `"status":"ok"`)
	assertResponse(t, client, http.MethodGet, baseURL+"/ready", "", nil, http.StatusOK, `"status":"ready"`)
	assertResponse(t, client, http.MethodGet, baseURL+"/success?access_token="+releaseSecret, "", nil, http.StatusOK, "release-ok")
	assertResponse(t, client, http.MethodPost, baseURL+"/validate", `{"secret":"`+releaseSecret+`"}`, map[string]string{"Content-Type": "application/json"}, http.StatusBadRequest, "Invalid request")
	assertResponse(t, client, http.MethodGet, baseURL+"/private", "", map[string]string{"Authorization": "Bearer " + releaseSecret}, http.StatusUnauthorized, "invalid or expired token")

	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("send SIGTERM: %v", err)
	}
	select {
	case err := <-waitDone:
		stopped = true
		if err != nil {
			t.Fatalf("application exited after SIGTERM: %v\n%s", err, stdout.String()+stderr.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatal("application did not exit within 15s after SIGTERM")
	}

	output := stdout.String() + stderr.String()
	if strings.Contains(output, releaseSecret) {
		t.Fatalf("logs or traces leaked request secret:\n%s", output)
	}
	for _, evidence := range []string{"Tracing enabled", "TraceID", "WhiteBear returning to ice"} {
		if !strings.Contains(output, evidence) {
			t.Fatalf("application output missing %q:\n%s", evidence, output)
		}
	}
}

func waitForLive(client *http.Client, url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		response, err := client.Get(url)
		if err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
			closeErr := response.Body.Close()
			if response.StatusCode == http.StatusOK && closeErr == nil {
				return nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("%s did not become live within %s", url, timeout)
}

func assertResponse(t *testing.T, client *http.Client, method, url, body string, headers map[string]string, wantStatus int, wantBody string) {
	t.Helper()
	request, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	contents, readErr := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("read response: read=%v close=%v", readErr, closeErr)
	}
	if response.StatusCode != wantStatus || !strings.Contains(string(contents), wantBody) {
		t.Fatalf("%s %s = %d %s, want status %d containing %q", method, url, response.StatusCode, contents, wantStatus, wantBody)
	}
	if strings.Contains(string(contents), releaseSecret) {
		t.Fatalf("%s %s leaked secret in response: %s", method, url, contents)
	}
}

func reservePort(t *testing.T) int {
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

func runGo(t *testing.T, directory string, args ...string) {
	t.Helper()
	command := exec.Command("go", args...)
	command.Dir = directory
	command.Env = commandEnvironment(nil)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("go %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}

func commandEnvironment(overrides map[string]string) []string {
	values := map[string]string{
		"GOSUMDB":     "sum.golang.org",
		"GOTOOLCHAIN": "go1.25.12",
	}
	for name, value := range overrides {
		values[name] = value
	}
	environment := make([]string, 0, len(os.Environ())+len(values))
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if _, replaced := values[name]; !replaced {
			environment = append(environment, entry)
		}
	}
	for name, value := range values {
		environment = append(environment, name+"="+value)
	}
	return environment
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve release E2E source path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0644); err != nil {
		t.Fatal(err)
	}
}

type lockedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (buffer *lockedBuffer) Write(contents []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.Write(contents)
}

func (buffer *lockedBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.String()
}

const legacyFixtureSource = `package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/duiniwukenaihe/gin-bear/pkg/bear"
)

type validationRequest struct {
	Name string ` + "`json:\"name\" binding:\"required\"`" + `
}

type legacyController struct{}

func (legacyController) Name() string { return "legacyController" }

func (legacyController) Build(app *bear.Bear) {
	app.Handle(http.MethodGet, "/success", func() map[string]string { return map[string]string{"result": "release-ok"} })
	app.POST("/validate", bear.Convert(func(request *validationRequest) map[string]string {
		return map[string]string{"name": request.Name}
	}))
	app.Handle(http.MethodGet, "/private", func() string { return "private" })
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	port, err := strconv.Atoi(os.Getenv("BEAR_E2E_PORT"))
	if err != nil { panic(err) }
	config := bear.NewSysConfig()
	config.Server.Port = int32(port)
	config.DB.Enabled = false
	config.Auth.JWTSecret = "release-e2e-jwt-secret-1234567890"
	config.Auth.PublicPaths = []string{"/live", "/ready", "/success", "/validate"}
	config.Tracing.Enabled = true
	config.Tracing.Exporter = "stdout"
	config.Tracing.ServiceName = "legacy-release-check"
	config.Tracing.SampleRate = 1
	app := bear.Ignite(config).EnableTracing(ctx).Mount("", legacyController{}).Attach(bear.NewAuthFairing()).EnableHealth()
	if err := app.ApplyAll(ctx); err != nil { panic(err) }
	if err := app.Launch(ctx); err != nil && !errors.Is(err, context.Canceled) {
		panic(fmt.Errorf("launch legacy app: %w", err))
	}
}
`

const generatedFixtureAppSource = `package app

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"github.com/duiniwukenaihe/gin-bear/pkg/bear"
)

type validationRequest struct {
	Name string ` + "`json:\"name\" binding:\"required\"`" + `
}

func Run(ctx context.Context) error {
	port, err := strconv.Atoi(os.Getenv("BEAR_E2E_PORT"))
	if err != nil { return err }
	config := bear.NewSysConfig()
	config.Server.Port = int32(port)
	config.DB.Enabled = false
	config.Auth.JWTSecret = "release-e2e-jwt-secret-1234567890"
	config.Auth.PublicPaths = []string{"/live", "/ready", "/success", "/validate"}
	config.Tracing.Enabled = true
	config.Tracing.Exporter = "stdout"
	config.Tracing.ServiceName = "generated-release-check"
	config.Tracing.SampleRate = 1
	application := bear.Ignite(config).EnableTracing(ctx).EnableHealth()
	application.Handle("GET", "/success", func() map[string]string { return map[string]string{"result": "release-ok"} })
	application.Handle("POST", "/validate", func(request *validationRequest) map[string]string {
		return map[string]string{"name": request.Name}
	})
	application.Handle("GET", "/private", func() string { return "private" })
	application.Attach(bear.NewAuthFairing())
	if err := application.ApplyAll(ctx); err != nil {
		return fmt.Errorf("initialize application: %w", err)
	}
	if err := application.Launch(ctx); err != nil {
		return fmt.Errorf("launch application: %w", err)
	}
	return nil
}
`

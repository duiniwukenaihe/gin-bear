//go:build !windows

package releasee2e

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
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
)

const releaseSecret = "task10-release-secret-7Yp3mQ9"
const maxApplicationLogBytes = 1 << 20

func TestStartApplicationRetriesAfterProcessExitsBeforeLive(t *testing.T) {
	directory := t.TempDir()
	marker := filepath.Join(directory, "first-attempt")
	binary := filepath.Join(directory, "retry-application")
	writeFile(t, binary, `#!/bin/sh
if mkdir "$BEAR_RELEASE_E2E_ATTEMPT_MARKER" 2>/dev/null; then
	exit 17
fi
exec "$BEAR_RELEASE_E2E_TEST_BINARY" -test.run '^TestReleaseE2ELiveHelper$'
`)
	if err := os.Chmod(binary, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEAR_RELEASE_E2E_ATTEMPT_MARKER", marker)
	t.Setenv("BEAR_RELEASE_E2E_TEST_BINARY", os.Args[0])
	t.Setenv("BEAR_RELEASE_E2E_LIVE_HELPER", "1")

	running, baseURL, client := startApplication(t, binary, directory)
	t.Cleanup(func() {
		if err := running.killAndWait(); err != nil {
			t.Errorf("stop retry application: %v", err)
		}
	})

	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("first application attempt did not run: %v", err)
	}
	assertResponse(t, client, http.MethodGet, baseURL+"/live", "", nil, http.StatusOK, "live")
}

func TestReleaseE2ELiveHelper(t *testing.T) {
	if os.Getenv("BEAR_RELEASE_E2E_LIVE_HELPER") != "1" {
		return
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/live", func(response http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(response, "live")
	})
	server := &http.Server{
		Addr:              "127.0.0.1:" + os.Getenv("BEAR_SERVER_PORT"),
		Handler:           mux,
		ReadHeaderTimeout: time.Second,
	}
	if err := server.ListenAndServe(); err != nil {
		t.Fatal(err)
	}
}

func TestKillAndWaitProcessReportsKillErrorWithPIDAndLogs(t *testing.T) {
	wait := &processWait{done: make(chan struct{})}
	err := killAndWaitProcess(4242, func() error { return errors.New("permission denied") }, wait, "bounded-output", 20*time.Millisecond)
	if err == nil {
		t.Fatal("killAndWaitProcess accepted Kill error")
	}
	for _, want := range []string{"PID 4242", "permission denied", "bounded-output"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Kill error missing %q: %v", want, err)
		}
	}
}

func TestKillAndWaitProcessTimesOutWithoutHanging(t *testing.T) {
	wait := &processWait{done: make(chan struct{})}
	started := time.Now()
	err := killAndWaitProcess(5252, func() error { return os.ErrProcessDone }, wait, "bounded-timeout-output", 20*time.Millisecond)
	if err == nil {
		t.Fatal("killAndWaitProcess unexpectedly completed without Wait result")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("killAndWaitProcess hung for %s", elapsed)
	}
	for _, want := range []string{"PID 5252", "bounded-timeout-output"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("timeout error missing %q: %v", want, err)
		}
	}
}

func TestReleaseCandidateApplications(t *testing.T) {
	if os.Getenv("BEAR_RELEASE_E2E") != "1" {
		t.Skip("release-only application E2E")
	}
	repository := repositoryRoot(t)
	bearCLI := buildFixture(t, repository, "./cmd/bear")

	t.Run("legacy-v0.9-style", func(t *testing.T) {
		directory := createLegacyFixture(t, repository)
		binary := buildFixture(t, directory, ".")
		exerciseApplication(t, binary, directory)
	})

	t.Run("newly-generated", func(t *testing.T) {
		directory := createGeneratedFixture(t, repository, bearCLI)
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

func createGeneratedFixture(t *testing.T, repository, bearCLI string) string {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "generated-release-check")
	runCLI(t, filepath.Dir(directory), bearCLI,
		"new", "generated-release-check",
		"--module", "example.com/generated-release-check",
		"--directory", directory,
		"--framework-version", "v0.10.0-rc.1",
	)
	appPath := filepath.Join(directory, "internal", "app", "app.go")
	generatedApp := readFile(t, appPath)
	writeFile(t, filepath.Join(directory, "internal", "app", "routes.go"), generatedFixtureRoutesSource)
	writeFile(t, filepath.Join(directory, "application.yaml"), generatedFixtureConfig)
	runGo(t, directory, "mod", "edit", "-replace", "github.com/duiniwukenaihe/gin-bear="+repository)
	runGo(t, directory, "mod", "tidy")
	generateFixtureResource(t, directory, bearCLI)
	runGo(t, directory, "mod", "tidy")
	runGo(t, directory, "test", "./...", "-count=1")
	if current := readFile(t, appPath); current != generatedApp {
		t.Fatalf("release E2E modified generated internal/app/app.go:\nbefore:\n%s\nafter:\n%s", generatedApp, current)
	}
	return directory
}

func generateFixtureResource(t *testing.T, directory, bearCLI string) {
	t.Helper()
	runCLI(t, directory, bearCLI, "gen", "api", "invoice", "--fields", "amount:decimal,published_at:datetime")
}

func runCLI(t *testing.T, directory, binary string, args ...string) {
	t.Helper()
	command := exec.Command(binary, args...)
	command.Dir = directory
	command.Env = commandEnvironment(nil)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("%s %s: %v\n%s", binary, strings.Join(args, " "), err, output)
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
	running, baseURL, client := startApplication(t, binary, directory)
	stopped := false
	defer func() {
		if stopped {
			return
		}
		if err := running.killAndWait(); err != nil {
			t.Errorf("force-stop application: %v", err)
		}
	}()

	assertResponse(t, client, http.MethodGet, baseURL+"/live", "", nil, http.StatusOK, `"status":"ok"`)
	assertResponse(t, client, http.MethodGet, baseURL+"/ready", "", nil, http.StatusOK, `"status":"ready"`)
	assertResponse(t, client, http.MethodGet, baseURL+"/success?access_token="+releaseSecret, "", nil, http.StatusOK, "release-ok")
	assertResponse(t, client, http.MethodPost, baseURL+"/validate", `{"secret":"`+releaseSecret+`"}`, map[string]string{"Content-Type": "application/json"}, http.StatusBadRequest, "Invalid request")
	assertResponse(t, client, http.MethodGet, baseURL+"/private", "", map[string]string{"Authorization": "Bearer " + releaseSecret}, http.StatusUnauthorized, "invalid or expired token")

	if err := running.command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("send SIGTERM: %v", err)
	}
	select {
	case <-running.wait.done:
		stopped = true
		running.waited = true
		err := running.wait.err
		if err != nil {
			t.Fatalf("application exited after SIGTERM: %v\n%s", err, running.output())
		}
	case <-time.After(15 * time.Second):
		t.Fatal("application did not exit within 15s after SIGTERM")
	}

	output := running.output()
	if strings.Contains(output, releaseSecret) {
		t.Fatalf("logs or traces leaked request secret:\n%s", output)
	}
	for _, evidence := range []string{"Tracing enabled", "TraceID", "WhiteBear returning to ice"} {
		if !strings.Contains(output, evidence) {
			t.Fatalf("application output missing %q:\n%s", evidence, output)
		}
	}
}

type runningApplication struct {
	command *exec.Cmd
	wait    *processWait
	stdout  *boundedBuffer
	stderr  *boundedBuffer
	waited  bool
}

func startApplication(t *testing.T, binary, directory string) (*runningApplication, string, *http.Client) {
	t.Helper()
	client := &http.Client{Timeout: 2 * time.Second}
	jwtSecret := randomJWTSecret(t)
	var failures []string
	for attempt := 1; attempt <= 3; attempt++ {
		port := reservePort(t)
		running := &runningApplication{
			command: exec.Command(binary),
			stdout:  newBoundedBuffer(maxApplicationLogBytes),
			stderr:  newBoundedBuffer(maxApplicationLogBytes),
		}
		running.command.Dir = directory
		running.command.Env = commandEnvironment(map[string]string{
			"BEAR_SERVER_PORT": strconv.Itoa(port),
			"BEAR_E2E_PORT":    strconv.Itoa(port),
			"BEAR_ENV":         "test",
			"GIN_MODE":         "release",
			"JWT_SECRET":       jwtSecret,
		})
		running.command.Stdout = running.stdout
		running.command.Stderr = running.stderr
		if err := running.command.Start(); err != nil {
			t.Fatal(err)
		}
		running.wait = waitForProcess(running.command)
		baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
		if err := waitForLive(client, baseURL+"/live", running.wait, 20*time.Second); err == nil {
			return running, baseURL, client
		} else {
			failures = append(failures, fmt.Sprintf("attempt %d: %v\n%s", attempt, err, running.output()))
		}
		if err := running.killAndWait(); err != nil {
			t.Fatalf("stop failed application attempt: %v", err)
		}
	}
	t.Fatalf("application failed to start after 3 complete attempts:\n%s", strings.Join(failures, "\n"))
	return nil, "", nil
}

func randomJWTSecret(t *testing.T) string {
	t.Helper()
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatalf("generate release JWT secret: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

type processWait struct {
	done chan struct{}
	err  error
}

func waitForProcess(command *exec.Cmd) *processWait {
	wait := &processWait{done: make(chan struct{})}
	go func() {
		wait.err = command.Wait()
		close(wait.done)
	}()
	return wait
}

func waitForLive(client *http.Client, url string, wait *processWait, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-wait.done:
			err := wait.err
			if err != nil {
				return fmt.Errorf("process exited before live: %w", err)
			}
			return fmt.Errorf("process exited before live")
		default:
		}
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

func readFile(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}

func (application *runningApplication) killAndWait() error {
	if application == nil || application.waited {
		return nil
	}
	if application.command == nil || application.command.Process == nil {
		return fmt.Errorf("cannot kill application without a process; bounded logs:\n%s", application.output())
	}
	err := killAndWaitProcess(
		application.command.Process.Pid,
		application.command.Process.Kill,
		application.wait,
		application.output(),
		5*time.Second,
	)
	if err != nil {
		return err
	}
	application.waited = true
	return nil
}

func killAndWaitProcess(pid int, kill func() error, wait *processWait, logs string, timeout time.Duration) error {
	if err := kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("kill application PID %d: %w; bounded logs:\n%s", pid, err, logs)
	}
	select {
	case <-wait.done:
		_ = wait.err
		return nil
	case <-time.After(timeout):
		return fmt.Errorf("application PID %d did not exit within %s after Kill; bounded logs:\n%s", pid, timeout, logs)
	}
}

func (application *runningApplication) output() string {
	return application.stdout.String() + application.stderr.String()
}

type boundedBuffer struct {
	mu        sync.Mutex
	buffer    bytes.Buffer
	remaining int64
}

func newBoundedBuffer(limit int64) *boundedBuffer {
	return &boundedBuffer{remaining: limit}
}

func (buffer *boundedBuffer) Write(contents []byte) (int, error) {
	written := len(contents)
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	if buffer.remaining <= 0 {
		return written, nil
	}
	if int64(len(contents)) > buffer.remaining {
		contents = contents[:buffer.remaining]
	}
	_, _ = buffer.buffer.Write(contents)
	buffer.remaining -= int64(len(contents))
	return written, nil
}

func (buffer *boundedBuffer) String() string {
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
	config.Auth.JWTSecret = os.Getenv("JWT_SECRET")
	publicPaths := []string{"/live", "/ready", "/success", "/validate"}
	config.Auth.PublicPaths = &publicPaths
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

const generatedFixtureRoutesSource = `package app

import (
	"github.com/duiniwukenaihe/gin-bear/pkg/bear"
)

type validationRequest struct {
	Name string ` + "`json:\"name\" binding:\"required\"`" + `
}

func configure(application *bear.Bear) {
	application.Handle("GET", "/success", func() map[string]string { return map[string]string{"result": "release-ok"} })
	application.Handle("POST", "/validate", func(request *validationRequest) map[string]string {
		return map[string]string{"name": request.Name}
	})
	application.Handle("GET", "/private", func() string { return "private" })
	application.Attach(bear.NewAuthFairing())
}
`

const generatedFixtureConfig = `server:
  port: 8080
  name: "generated-release-check"
  shutdown_timeout: "5s"

database:
  enabled: false

tracing:
  enabled: true
  service_name: "generated-release-check"
  exporter: "stdout"
  sample_rate: 1.0

auth:
  jwt_secret: "replace-with-at-least-32-random-characters"
  token_expire_hours: 24
  public_paths:
    - "/live"
    - "/ready"
    - "/success"
    - "/validate"

metrics:
  enabled: true
  path: "/metrics"
`

package bear

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

type httpShutdownLifecycleProbe struct {
	stopping chan struct{}
	once     sync.Once
}

func (*httpShutdownLifecycleProbe) Name() string { return "http-shutdown-lifecycle-probe" }
func (*httpShutdownLifecycleProbe) Init(context.Context) error {
	return nil
}
func (p *httpShutdownLifecycleProbe) Shutdown() error {
	p.once.Do(func() { close(p.stopping) })
	return nil
}

func TestServeDoesNotCloseLifecycleResourcesWhileHTTPHandlerIgnoresCancellation(t *testing.T) {
	const shutdownBudget = 400 * time.Millisecond
	port := availableTCPPort(t)
	config := NewSysConfig()
	config.Server.Port = int32(port)
	config.Server.ShutdownTimeout = shutdownBudget.String()

	probe := &httpShutdownLifecycleProbe{stopping: make(chan struct{})}
	app := Ignite(config).Beans(probe)
	entered := make(chan struct{})
	finished := make(chan struct{})
	release := make(chan struct{})
	var enterOnce sync.Once
	var finishOnce sync.Once
	var releaseOnce sync.Once
	releaseHandler := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseHandler)

	app.GET("/block", func(ctx *gin.Context) {
		enterOnce.Do(func() { close(entered) })
		<-release
		finishOnce.Do(func() { close(finished) })
		ctx.Status(http.StatusNoContent)
	})

	serveCtx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() { serveDone <- app.Serve(serveCtx) }()
	t.Cleanup(cancel)

	requestDone := make(chan error, 1)
	go func() {
		url := fmt.Sprintf("http://127.0.0.1:%d/block", port)
		deadline := time.Now().Add(time.Second)
		for {
			response, err := http.Get(url)
			if err == nil {
				_ = response.Body.Close()
				requestDone <- nil
				return
			}
			if time.Now().After(deadline) {
				requestDone <- err
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	waitHTTPShutdownSignal(t, entered, time.Second, "HTTP handler did not enter")
	shutdownStarted := time.Now()
	cancel()
	serveErr := waitHTTPShutdownError(t, serveDone, shutdownBudget+300*time.Millisecond)
	if serveErr == nil || !strings.Contains(serveErr.Error(), "active HTTP handlers") {
		t.Fatalf("Serve() error = %v, want active HTTP handler shutdown error", serveErr)
	}
	assertHTTPShutdownSignalOpen(t, finished, "non-cooperative HTTP handler unexpectedly finished")
	assertHTTPShutdownSignalOpen(t, probe.stopping, "Lifecycle resources closed while an HTTP handler was still active")
	if elapsed := time.Since(shutdownStarted); elapsed > shutdownBudget+300*time.Millisecond {
		t.Fatalf("shutdown took %s, want at most %s", elapsed, shutdownBudget+300*time.Millisecond)
	}

	deferredShutdownStarted := time.Now()
	shutdownErr := app.Shutdown(context.Background())
	if shutdownErr == nil || !strings.Contains(shutdownErr.Error(), "active HTTP handlers") {
		t.Fatalf("deferred Shutdown() error = %v, want active HTTP handler error", shutdownErr)
	}
	if elapsed := time.Since(deferredShutdownStarted); elapsed > 100*time.Millisecond {
		t.Fatalf("deferred Shutdown() took %s, want a fast failure after handler timeout", elapsed)
	}
	assertHTTPShutdownSignalOpen(t, probe.stopping, "Deferred Shutdown closed resources while an HTTP handler was still active")

	releaseHandler()
	waitHTTPShutdownSignal(t, finished, time.Second, "HTTP handler did not finish after release")
	select {
	case <-requestDone:
	case <-time.After(time.Second):
		t.Fatal("HTTP client did not return after handler release")
	}
	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() after handler release error = %v", err)
	}
	waitHTTPShutdownSignal(t, probe.stopping, time.Second, "Lifecycle resources did not close after HTTP handler release")
}

func waitHTTPShutdownSignal(t *testing.T, signal <-chan struct{}, timeout time.Duration, message string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(timeout):
		t.Fatal(message)
	}
}

func assertHTTPShutdownSignalOpen(t *testing.T, signal <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-signal:
		t.Fatal(message)
	default:
	}
}

func waitHTTPShutdownError(t *testing.T, result <-chan error, timeout time.Duration) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(timeout):
		t.Fatal("Serve() did not return within the shutdown budget")
		return nil
	}
}

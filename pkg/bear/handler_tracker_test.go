package bear

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestActiveHandlerTrackerRejectsLateHandlersAfterShutdownBegins(t *testing.T) {
	tracker := newActiveHandlerTracker()
	if !tracker.begin() {
		t.Fatal("initial handler was rejected")
	}
	tracker.stopAccepting()
	tracker.end()
	if err := tracker.wait(context.Background()); err != nil {
		t.Fatalf("wait() error = %v", err)
	}
	if tracker.begin() {
		t.Fatal("handler entered after shutdown began")
	}
	if count := tracker.count(); count != 0 {
		t.Fatalf("active handler count = %d, want 0", count)
	}
}

func TestShutdownRejectsNewHTTPHandlersBeforeWaitingForActiveOnes(t *testing.T) {
	resetGinModeForTest(t)
	config := NewSysConfig()
	config.SetFrameworkStrict(true)
	config.Server.ShutdownTimeout = "100ms"
	app := Ignite(config)
	entered := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	app.GET("/block", func(ctx *gin.Context) {
		close(entered)
		<-release
		ctx.Status(http.StatusNoContent)
	})
	if err := app.ApplyAll(context.Background()); err != nil {
		t.Fatalf("ApplyAll() error = %v", err)
	}

	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		response := httptest.NewRecorder()
		app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/block", nil))
	}()
	<-entered

	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- app.Shutdown(shutdownCtx) }()

	deadline := time.Now().Add(time.Second)
	for {
		app.httpHandlers.mu.Lock()
		closed := app.httpHandlers.closed
		app.httpHandlers.mu.Unlock()
		if closed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Shutdown() did not close the HTTP handler admission gate")
		}
		time.Sleep(time.Millisecond)
	}
	responseDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		response := httptest.NewRecorder()
		app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/block", nil))
		responseDone <- response
	}()
	select {
	case response := <-responseDone:
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("request after Shutdown() began status = %d, want %d", response.Code, http.StatusServiceUnavailable)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("request entered the handler after Shutdown() began")
	}
	shutdownErr := <-shutdownDone
	if shutdownErr == nil || !strings.Contains(shutdownErr.Error(), "active HTTP handlers") {
		t.Fatalf("Shutdown() error = %v, want active HTTP handler error", shutdownErr)
	}

	close(release)
	<-firstDone
	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() after handler release error = %v", err)
	}
}

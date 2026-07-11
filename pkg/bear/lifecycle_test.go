package bear

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

type namedBean struct {
	name string
}

func (b *namedBean) Name() string { return b.name }

type recordingComponent struct {
	name   string
	events *[]string
}

func (c recordingComponent) Name() string { return c.name }

func (c recordingComponent) Init(context.Context) error {
	*c.events = append(*c.events, "start:"+c.name)
	return nil
}

func (c recordingComponent) ShutdownContext(context.Context) error {
	*c.events = append(*c.events, "stop:"+c.name)
	return nil
}

type failingShutdownComponent struct {
	name string
	err  error
}

func (c failingShutdownComponent) Name() string { return c.name }

func (c failingShutdownComponent) ShutdownContext(context.Context) error {
	return c.err
}

type readinessBean struct {
	name string
	err  error
}

func (b *readinessBean) Name() string { return b.name }

func (b *readinessBean) CheckReady(context.Context) error { return b.err }

type blockingGRPCServer struct {
	stopped chan struct{}
	once    sync.Once
}

func (s *blockingGRPCServer) GracefulStop() { <-s.stopped }

func (s *blockingGRPCServer) Stop() {
	s.once.Do(func() { close(s.stopped) })
}

func TestApplicationsDoNotResolveEachOthersBeans(t *testing.T) {
	a := Ignite(NewSysConfig())
	b := Ignite(NewSysConfig())
	a.Runtime().Container.Set(&namedBean{name: "a"})
	b.Runtime().Container.Set(&namedBean{name: "b"})

	if got := Resolve[*namedBean](a.Runtime().Container).name; got != "a" {
		t.Fatalf("app a resolved %q", got)
	}
	if got := Resolve[*namedBean](b.Runtime().Container).name; got != "b" {
		t.Fatalf("app b resolved %q", got)
	}
	if got := GetByType[*namedBean]().name; got != "b" {
		t.Fatalf("legacy facade resolved %q, want latest application bean", got)
	}
}

func TestApplicationsKeepHealthAndMetricsScoped(t *testing.T) {
	a := Ignite(NewSysConfig())
	b := Ignite(NewSysConfig())
	a.Beans(&readinessBean{name: "a-only", err: errors.New("down")}).EnableHealth()
	b.EnableHealth()
	requireNoError(t, a.ApplyAll(context.Background()))
	requireNoError(t, b.ApplyAll(context.Background()))

	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	aResponse := httptest.NewRecorder()
	a.ServeHTTP(aResponse, req)
	if aResponse.Code != http.StatusServiceUnavailable || !strings.Contains(aResponse.Body.String(), "a-only") {
		t.Fatalf("app a readiness = %d %s", aResponse.Code, aResponse.Body.String())
	}

	bResponse := httptest.NewRecorder()
	b.ServeHTTP(bResponse, httptest.NewRequest(http.MethodGet, "/ready", nil))
	if bResponse.Code != http.StatusOK || strings.Contains(bResponse.Body.String(), "a-only") {
		t.Fatalf("app b readiness = %d %s", bResponse.Code, bResponse.Body.String())
	}

	a.GET("/a-only", func(ctx *gin.Context) { ctx.Status(http.StatusNoContent) })
	a.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/a-only", nil))
	metricsResponse := httptest.NewRecorder()
	b.ServeHTTP(metricsResponse, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if strings.Contains(metricsResponse.Body.String(), "/a-only") {
		t.Fatalf("app b metrics include app a route: %s", metricsResponse.Body.String())
	}
}

func TestLifecycleInitializesFIFOAndShutsDownLIFO(t *testing.T) {
	var events []string
	l := newLifecycle()
	l.Add(recordingComponent{"first", &events})
	l.Add(recordingComponent{"second", &events})
	requireNoError(t, l.Start(context.Background()))
	requireNoError(t, l.Stop(context.Background()))
	assertStrings(t, events, []string{"start:first", "start:second", "stop:second", "stop:first"})
}

func TestLifecycleJoinsShutdownErrors(t *testing.T) {
	firstErr := errors.New("first failed")
	secondErr := errors.New("second failed")
	l := newLifecycle()
	l.Add(failingShutdownComponent{name: "first", err: firstErr})
	l.Add(failingShutdownComponent{name: "second", err: secondErr})
	requireNoError(t, l.Start(context.Background()))

	err := l.Stop(context.Background())
	if !errors.Is(err, firstErr) || !errors.Is(err, secondErr) {
		t.Fatalf("Stop() error = %v, want both shutdown errors", err)
	}
}

func TestRuntimeContainerRegistrationsParticipateInLifecycle(t *testing.T) {
	var events []string
	app := Ignite(NewSysConfig())
	app.Runtime().Container.Set(recordingComponent{name: "direct", events: &events})

	requireNoError(t, app.ApplyAll(context.Background()))
	requireNoError(t, app.Runtime().Lifecycle.Stop(context.Background()))
	assertStrings(t, events, []string{"start:direct", "stop:direct"})
}

func TestLaunchClosesHTTPListenerWhenGRPCBindingFails(t *testing.T) {
	var events []string
	port := availableTCPPort(t)
	cfg := NewSysConfig()
	cfg.Server.Port = int32(port)
	cfg.GRPC.Enabled = true
	cfg.GRPC.Port = int32(port)
	app := Ignite(cfg)
	app.Beans(recordingComponent{name: "launch", events: &events})
	requireNoError(t, app.ApplyAll(context.Background()))

	err := app.Launch(context.Background())
	if err == nil || !strings.Contains(err.Error(), "gRPC") {
		t.Fatalf("Launch() error = %v, want gRPC bind failure", err)
	}

	listener, listenErr := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if listenErr != nil {
		t.Fatalf("HTTP listener was not cleaned up: %v", listenErr)
	}
	requireNoError(t, listener.Close())
	assertStrings(t, events, []string{"start:launch", "stop:launch"})
}

func TestGRPCShutdownFallsBackToStopAtDeadline(t *testing.T) {
	server := &blockingGRPCServer{stopped: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	err := shutdownGRPCServer(ctx, server)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdownGRPCServer() error = %v, want deadline exceeded", err)
	}
	select {
	case <-server.stopped:
	default:
		t.Fatal("fallback Stop was not called")
	}
}

func availableTCPPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	requireNoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	requireNoError(t, listener.Close())
	return port
}

func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func assertStrings(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("events = %v, want %v", got, want)
		}
	}
}

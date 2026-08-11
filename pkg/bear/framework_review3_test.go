package bear

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

type review3Dependency struct{}

func (*review3Dependency) Name() string { return "review3Dependency" }

type review3Service struct {
	Dependency *review3Dependency `inject:"-"`
}

func (*review3Service) Name() string { return "review3Service" }

type review3Module struct {
	service *review3Service
}

func (*review3Module) Name() string { return "review3Module" }
func (m *review3Module) Beans() []Bean {
	return []Bean{m.service}
}
func (*review3Module) Build(*Bear) {}
func (m *review3Module) BuildE(*Bear) error {
	if m.service.Dependency == nil {
		return errors.New("module bean dependency was not injected before BuildE")
	}
	return nil
}

type review3MissingDependency struct{}

type review3RouteFairing struct {
	BaseFairing
	Dependency *review3MissingDependency `inject:"-"`
}

type review3WebSocketHandler struct {
	BaseWebSocketHandler
	Dependency *review3MissingDependency `inject:"-"`
}

type review3CancelRollbackComponent struct {
	entered     chan struct{}
	rollbackErr error
}

func (*review3CancelRollbackComponent) Name() string { return "review3CancelRollbackComponent" }
func (c *review3CancelRollbackComponent) Init(ctx context.Context) error {
	close(c.entered)
	<-ctx.Done()
	return ctx.Err()
}
func (c *review3CancelRollbackComponent) ShutdownContext(context.Context) error {
	return c.rollbackErr
}

func TestStrictModuleBuildSeesInjectedModuleBeans(t *testing.T) {
	resetGinModeForTest(t)
	config := NewSysConfig()
	config.SetFrameworkStrict(true)
	app := Ignite(config)
	if err := app.BeansE(&review3Dependency{}); err != nil {
		t.Fatal(err)
	}
	module := &review3Module{service: &review3Service{}}
	if err := app.AddModuleE(module); err != nil {
		t.Fatal(err)
	}
	if err := app.ApplyAll(context.Background()); err != nil {
		t.Fatalf("ApplyAll() error = %v", err)
	}
}

func TestStrictPluginBuildSeesInjectedModuleBeans(t *testing.T) {
	resetGinModeForTest(t)
	config := NewSysConfig()
	config.SetFrameworkStrict(true)
	app := Ignite(config)
	if err := app.BeansE(&review3Dependency{}); err != nil {
		t.Fatal(err)
	}
	module := &review3Module{service: &review3Service{}}
	if err := app.pluginManager.registerModule(module); err != nil {
		t.Fatalf("registerModule() error = %v", err)
	}
}

func TestStrictRouteFairingMissingDependencyFailsStartup(t *testing.T) {
	resetGinModeForTest(t)
	config := NewSysConfig()
	config.SetFrameworkStrict(true)
	app := Ignite(config)
	app.HandleWithFairing(http.MethodGet, "/strict", func() string { return "ok" }, &review3RouteFairing{})

	err := app.ApplyAll(context.Background())
	if !errors.Is(err, ErrBeanMissing) {
		t.Fatalf("ApplyAll() error = %v, want ErrBeanMissing", err)
	}
}

func TestStrictWebSocketHandlerMissingDependencyFailsStartup(t *testing.T) {
	resetGinModeForTest(t)
	config := NewSysConfig()
	config.SetFrameworkStrict(true)
	config.WS.SetAllowedOrigins([]string{"https://app.example.com"})
	app := Ignite(config)
	app.HandleWS("/ws", &review3WebSocketHandler{})

	err := app.ApplyAll(context.Background())
	if !errors.Is(err, ErrBeanMissing) {
		t.Fatalf("ApplyAll() error = %v, want ErrBeanMissing", err)
	}
}

func TestServePreservesRollbackFailureJoinedWithCancellation(t *testing.T) {
	resetGinModeForTest(t)
	rollbackErr := errors.New("rollback failed after cancellation")
	component := &review3CancelRollbackComponent{
		entered:     make(chan struct{}),
		rollbackErr: rollbackErr,
	}
	config := NewSysConfig()
	config.SetFrameworkStrict(true)
	app := Ignite(config)
	if err := app.BeansE(component); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- app.Serve(ctx) }()
	<-component.entered
	cancel()

	if err := <-result; !errors.Is(err, rollbackErr) {
		t.Fatalf("Serve() error = %v, want rollback failure", err)
	}
}

func TestCompatibilityRuntimeCannotMutateEstablishedStrictGinMode(t *testing.T) {
	ginRuntimeMu.Lock()
	strictMode := strictGinRuntimeMode
	ginRuntimeMu.Unlock()
	if strictMode == "" {
		strictMode = gin.DebugMode
		config := NewSysConfig()
		config.SetFrameworkStrict(true)
		config.Server.Mode = strictMode
		if _, err := IgniteE(config); err != nil {
			t.Fatalf("establish strict Gin mode: %v", err)
		}
	}
	conflictingMode := gin.TestMode
	if strictMode == gin.TestMode {
		conflictingMode = gin.DebugMode
	}
	config := NewSysConfig()
	config.Server.Mode = conflictingMode
	before := gin.Mode()
	if _, err := IgniteE(config); !errors.Is(err, ErrGinRuntimeConflict) {
		t.Fatalf("compatibility IgniteE() error = %v, want ErrGinRuntimeConflict", err)
	}
	if got := gin.Mode(); got != before {
		t.Fatalf("compatibility IgniteE changed Gin mode from %q to %q", before, got)
	}
}

func TestServeRejectsSequentialRestartAfterShutdown(t *testing.T) {
	config := NewSysConfig()
	config.Server.Port = int32(availableTCPPort(t))
	component := &countedLifecycleComponent{name: "single-use-serve"}
	app := Ignite(config).Beans(component)
	ctx, cancel := context.WithCancel(context.Background())
	firstDone := make(chan error, 1)
	go func() { firstDone <- app.Serve(ctx) }()
	waitForAtomicCount(t, &component.starts, 1, "first Serve component start")
	cancel()
	if err := <-firstDone; err != nil {
		t.Fatalf("first Serve() error = %v", err)
	}

	secondDone := make(chan error, 1)
	go func() { secondDone <- app.Serve(context.Background()) }()
	select {
	case err := <-secondDone:
		if !errors.Is(err, ErrAlreadyServing) {
			t.Fatalf("second Serve() error = %v, want ErrAlreadyServing", err)
		}
	case <-time.After(time.Second):
		t.Fatal("second Serve() started a server after lifecycle shutdown")
	}
}

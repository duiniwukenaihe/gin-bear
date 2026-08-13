package bear

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

var (
	_ func(*BeanFactory, any) error      = (*BeanFactory).TrySet
	_ func(*BeanFactory, any, any) error = (*BeanFactory).TrySetWithInterface
	_ func(*BeanFactory, any, any)       = (*BeanFactory).SetWithInterface
)

type lifecycleReviewAlias interface {
	Bean
}

type inFlightLaunchModule struct {
	bean         Bean
	beansEntered chan struct{}
	releaseBeans chan struct{}
}

func (m *inFlightLaunchModule) Name() string { return "in-flight-launch" }

func (m *inFlightLaunchModule) Beans() []Bean {
	close(m.beansEntered)
	<-m.releaseBeans
	return []Bean{m.bean}
}

func (*inFlightLaunchModule) Build(*Bear) {}

func TestDirectLaunchStartsAndStopsRegisteredLifecycleComponents(t *testing.T) {
	config := NewSysConfig()
	config.Server.Port = int32(availableTCPPort(t))
	component := &countedLifecycleComponent{name: "direct-launch"}
	var hookCalls atomic.Int32
	app := Ignite(config).Beans(component)
	app.OnShutdown(func() { hookCalls.Add(1) })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	launchDone := make(chan error, 1)
	go func() { launchDone <- app.Launch(ctx) }()

	waitForAtomicCount(t, &component.starts, 1, "direct Launch component start")
	cancel()
	if err := <-launchDone; err != nil {
		t.Fatalf("Launch() error = %v", err)
	}
	if got := component.stops.Load(); got != 1 {
		t.Fatalf("component stops = %d, want 1", got)
	}
	if got := hookCalls.Load(); got != 1 {
		t.Fatalf("shutdown hook calls = %d, want 1", got)
	}
}

func TestServeStartsAndStopsWithoutInstallingSignalHandlers(t *testing.T) {
	config := NewSysConfig()
	config.Server.Port = int32(availableTCPPort(t))
	component := &countedLifecycleComponent{name: "direct-serve"}
	app := Ignite(config).Beans(component)
	ctx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() { serveDone <- app.Serve(ctx) }()

	waitForAtomicCount(t, &component.starts, 1, "direct Serve component start")
	cancel()
	if err := <-serveDone; err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
	if got := component.stops.Load(); got != 1 {
		t.Fatalf("component stops = %d, want 1", got)
	}
}

func TestConcurrentApplyAndServeShareOneInitialization(t *testing.T) {
	config := NewSysConfig()
	config.Server.Port = int32(availableTCPPort(t))
	component := &gatedInitializer{
		name:    "concurrent-apply-serve",
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	app := Ignite(config).Beans(component)
	applyDone := make(chan error, 1)
	go func() { applyDone <- app.ApplyAll(context.Background()) }()
	<-component.entered

	ctx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() { serveDone <- app.Serve(ctx) }()
	close(component.release)
	if err := <-applyDone; err != nil {
		cancel()
		<-serveDone
		t.Fatalf("ApplyAll() error = %v", err)
	}
	if got := component.starts.Load(); got != 1 {
		cancel()
		<-serveDone
		t.Fatalf("initializer calls = %d, want 1", got)
	}
	cancel()
	if err := <-serveDone; err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
}

func TestSecondServeReturnsAlreadyServingWithoutStoppingOwner(t *testing.T) {
	config := NewSysConfig()
	config.Server.Port = int32(availableTCPPort(t))
	component := &countedLifecycleComponent{name: "serve-owner"}
	app := Ignite(config).Beans(component)
	ctx, cancel := context.WithCancel(context.Background())
	firstDone := make(chan error, 1)
	go func() { firstDone <- app.Serve(ctx) }()
	waitForAtomicCount(t, &component.starts, 1, "first Serve component start")

	if err := app.Serve(context.Background()); !errors.Is(err, ErrAlreadyServing) {
		cancel()
		<-firstDone
		t.Fatalf("second Serve() error = %v, want ErrAlreadyServing", err)
	}
	if got := component.stops.Load(); got != 0 {
		cancel()
		<-firstDone
		t.Fatalf("rejected Serve stopped owner lifecycle %d times", got)
	}
	cancel()
	if err := <-firstDone; err != nil {
		t.Fatalf("first Serve() error = %v", err)
	}
}

func TestServeIsRejectedAfterShutdownCompletesBeforeServe(t *testing.T) {
	config := NewSysConfig()
	config.Server.Port = int32(availableTCPPort(t))
	component := &countedLifecycleComponent{name: "shutdown-before-serve"}
	app := Ignite(config).Beans(component)
	if err := app.ApplyAll(context.Background()); err != nil {
		t.Fatalf("ApplyAll() error = %v", err)
	}
	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}

	if err := app.Serve(context.Background()); !errors.Is(err, ErrAlreadyServing) {
		t.Fatalf("Serve() after Shutdown() error = %v, want ErrAlreadyServing", err)
	}
	if got := component.starts.Load(); got != 1 {
		t.Fatalf("component starts = %d, want 1", got)
	}
}

func TestSecondLaunchDoesNotStopFirstServingOwner(t *testing.T) {
	config := NewSysConfig()
	config.Server.Port = int32(availableTCPPort(t))
	component := &countedLifecycleComponent{name: "launch-owner"}
	app := Ignite(config).Beans(component)
	ctx, cancel := context.WithCancel(context.Background())
	firstDone := make(chan error, 1)
	go func() { firstDone <- app.Launch(ctx) }()
	waitForAtomicCount(t, &component.starts, 1, "first Launch component start")

	if err := app.Launch(context.Background()); !errors.Is(err, ErrAlreadyServing) {
		cancel()
		<-firstDone
		t.Fatalf("second Launch() error = %v, want ErrAlreadyServing", err)
	}
	if got := component.stops.Load(); got != 0 {
		cancel()
		<-firstDone
		t.Fatalf("rejected Launch stopped owner lifecycle %d times", got)
	}
	cancel()
	if err := <-firstDone; err != nil {
		t.Fatalf("first Launch() error = %v", err)
	}
}

func TestGinRuntimeConflictIsRejectedBeforeGlobalModeMutation(t *testing.T) {
	firstMode := "debug"
	ginRuntimeMu.Lock()
	if strictGinRuntimeMode != "" {
		firstMode = strictGinRuntimeMode
	}
	ginRuntimeMu.Unlock()
	first := NewSysConfig()
	first.SetFrameworkStrict(true)
	first.Server.Mode = firstMode
	if _, err := IgniteE(first); err != nil {
		t.Fatalf("first IgniteE() error = %v", err)
	}

	conflictingMode := gin.TestMode
	if firstMode == gin.TestMode {
		conflictingMode = "debug"
	}
	second := NewSysConfig()
	second.SetFrameworkStrict(true)
	second.Server.Mode = conflictingMode
	before := gin.Mode()
	if _, err := IgniteE(second); !errors.Is(err, ErrGinRuntimeConflict) {
		t.Fatalf("conflicting IgniteE() error = %v, want ErrGinRuntimeConflict", err)
	}
	if got := gin.Mode(); got != before {
		t.Fatalf("conflicting IgniteE changed gin mode from %q to %q", before, got)
	}
}

func TestDirectLaunchStartsInFlightPluginLifecycleBean(t *testing.T) {
	config := NewSysConfig()
	config.Server.Port = int32(availableTCPPort(t))
	component := &countedLifecycleComponent{name: "in-flight-plugin"}
	app := Ignite(config)
	module := &inFlightLaunchModule{
		bean:         component,
		beansEntered: make(chan struct{}),
		releaseBeans: make(chan struct{}),
	}
	registrationDone := make(chan error, 1)
	go func() { registrationDone <- app.pluginManager.registerModule(module) }()
	<-module.beansEntered

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	launchDone := make(chan error, 1)
	go func() { launchDone <- app.Launch(ctx) }()
	waitForPluginBarrierClosing(t, app.pluginBarrier)
	close(module.releaseBeans)
	if err := <-registrationDone; err != nil {
		cancel()
		<-launchDone
		t.Fatalf("in-flight plugin registration error = %v", err)
	}

	waitForAtomicCount(t, &component.starts, 1, "in-flight plugin component start")
	cancel()
	if err := <-launchDone; err != nil {
		t.Fatalf("Launch() error = %v", err)
	}
	if got := component.stops.Load(); got != 1 {
		t.Fatalf("component stops = %d, want 1", got)
	}
}

func TestDirectLaunchInitializerFailurePreventsListening(t *testing.T) {
	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	initErr := errors.New("direct launch init failed")
	component := &gatedFailingInitializer{
		name:    "direct-launch-failure",
		err:     initErr,
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	close(component.release)
	config := NewSysConfig()
	config.Server.Port = int32(port)
	app := Ignite(config).Beans(component)

	err = app.Launch(context.Background())
	if !errors.Is(err, initErr) {
		t.Fatalf("Launch() error = %v, want initializer failure before listen on %s", err, fmt.Sprint(listener.Addr()))
	}
}

func TestContainerSetAfterApplyAllIsRejectedWithoutMutation(t *testing.T) {
	app := Ignite(NewSysConfig())
	if err := app.ApplyAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	late := &countedLifecycleComponent{name: "late-after-apply"}

	app.Runtime().Container.Set(late)

	if got := Resolve[*countedLifecycleComponent](app.Runtime().Container); got != nil {
		t.Fatalf("Set() published bean after ApplyAll: %p", got)
	}
	if err := app.Runtime().Lifecycle.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if late.starts.Load() != 0 || late.stops.Load() != 0 {
		t.Fatalf("rejected bean starts/stops = %d/%d, want 0/0", late.starts.Load(), late.stops.Load())
	}
}

func TestContainerTrySetReportsClosedLifecycleWithoutMutation(t *testing.T) {
	app := Ignite(NewSysConfig())
	if err := app.ApplyAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	late := &countedLifecycleComponent{name: "late-try-set"}

	err := app.Runtime().Container.TrySet(late)

	if !errors.Is(err, ErrLifecycleRegistrationClosed) {
		t.Fatalf("TrySet() error = %v, want ErrLifecycleRegistrationClosed", err)
	}
	if got := Resolve[*countedLifecycleComponent](app.Runtime().Container); got != nil {
		t.Fatalf("TrySet() published rejected bean: %p", got)
	}
	if err := app.Runtime().Lifecycle.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestContainerRejectsSetWhileLifecycleIsStarting(t *testing.T) {
	runtime := newRuntime(NewSysConfig())
	blocking := &gatedInitializer{
		name:    "starting",
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	runtime.Container.Set(blocking)
	startDone := make(chan error, 1)
	go func() { startDone <- runtime.Lifecycle.Start(context.Background()) }()
	<-blocking.entered

	late := &countedLifecycleComponent{name: "late-while-starting"}
	if err := runtime.Container.TrySet(late); !errors.Is(err, ErrLifecycleRegistrationClosed) {
		close(blocking.release)
		t.Fatalf("TrySet() while starting error = %v, want ErrLifecycleRegistrationClosed", err)
	}
	runtime.Container.Set(late)
	if got := Resolve[*countedLifecycleComponent](runtime.Container); got != nil {
		close(blocking.release)
		t.Fatalf("Set() published bean while lifecycle was starting: %p", got)
	}

	close(blocking.release)
	if err := <-startDone; err != nil {
		t.Fatal(err)
	}
	if err := runtime.Lifecycle.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestContainerInterfaceRegistrationCannotBypassLifecycleSeal(t *testing.T) {
	runtime := newRuntime(NewSysConfig())
	if err := runtime.Lifecycle.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	late := &countedLifecycleComponent{name: "late-interface"}

	err := runtime.Container.TrySetWithInterface((*lifecycleReviewAlias)(nil), late)
	if !errors.Is(err, ErrLifecycleRegistrationClosed) {
		t.Fatalf("TrySetWithInterface() error = %v, want ErrLifecycleRegistrationClosed", err)
	}
	runtime.Container.SetWithInterface((*lifecycleReviewAlias)(nil), late)
	if got := Resolve[lifecycleReviewAlias](runtime.Container); got != nil {
		t.Fatalf("SetWithInterface() published bean after lifecycle start: %T", got)
	}
	if err := runtime.Lifecycle.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestContainerSetAfterDirectLaunchIsRejectedWithoutMutation(t *testing.T) {
	config := NewSysConfig()
	config.Server.Port = int32(availableTCPPort(t))
	app := Ignite(config)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	launchDone := make(chan error, 1)
	go func() { launchDone <- app.Launch(ctx) }()
	waitForPluginBarrierClosed(t, app.pluginBarrier)

	late := &countedLifecycleComponent{name: "late-after-launch"}
	app.Runtime().Container.Set(late)
	if got := Resolve[*countedLifecycleComponent](app.Runtime().Container); got != nil {
		cancel()
		<-launchDone
		t.Fatalf("Set() published bean after direct Launch: %p", got)
	}

	cancel()
	if err := <-launchDone; err != nil {
		t.Fatalf("Launch() error = %v", err)
	}
	if late.starts.Load() != 0 || late.stops.Load() != 0 {
		t.Fatalf("rejected bean starts/stops = %d/%d, want 0/0", late.starts.Load(), late.stops.Load())
	}
}

func waitForAtomicCount(t *testing.T, count *atomic.Int32, want int32, operation string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if count.Load() == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("%s count = %d, want %d", operation, count.Load(), want)
}

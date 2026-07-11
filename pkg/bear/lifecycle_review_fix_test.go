package bear

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var (
	_ func(*Lifecycle, any)  = (*Lifecycle).Add
	_ func(*Bear, ...func()) = (*Bear).OnShutdown
)

type gatedInitializer struct {
	name    string
	entered chan struct{}
	release chan struct{}
	starts  atomic.Int32
}

func (c *gatedInitializer) Name() string { return c.name }

func (c *gatedInitializer) Init(context.Context) error {
	if c.starts.Add(1) == 1 {
		close(c.entered)
	}
	<-c.release
	return nil
}

type countedLifecycleComponent struct {
	name   string
	starts atomic.Int32
	stops  atomic.Int32
}

type panickingInitializer struct {
	name string
}

func (c panickingInitializer) Name() string { return c.name }

func (c panickingInitializer) Init(context.Context) error {
	panic("init exploded")
}

type orderedLifecycleComponent struct {
	name   string
	events *[]string
	mu     *sync.Mutex
}

type gatedShutdownHookModule struct {
	beansEntered chan struct{}
	releaseBeans chan struct{}
	hookErr      chan error
}

func (m *gatedShutdownHookModule) Name() string { return "gated-shutdown-hook" }

func (m *gatedShutdownHookModule) Beans() []Bean {
	close(m.beansEntered)
	<-m.releaseBeans
	return nil
}

func (m *gatedShutdownHookModule) Build(app *Bear) {
	m.hookErr <- app.TryOnShutdown(func() {})
}

func (c orderedLifecycleComponent) Name() string { return c.name }

func (c orderedLifecycleComponent) Init(context.Context) error {
	c.mu.Lock()
	*c.events = append(*c.events, "init "+c.name)
	c.mu.Unlock()
	return nil
}

func (c orderedLifecycleComponent) Shutdown() error {
	c.mu.Lock()
	*c.events = append(*c.events, "stop "+c.name)
	c.mu.Unlock()
	return nil
}

func (c *countedLifecycleComponent) Name() string { return c.name }

func (c *countedLifecycleComponent) Init(context.Context) error {
	c.starts.Add(1)
	return nil
}

func (c *countedLifecycleComponent) Shutdown() error {
	c.stops.Add(1)
	return nil
}

func TestLifecycleInitPanicReturnsErrorAndRollsBack(t *testing.T) {
	var events []string
	var eventsMu sync.Mutex
	lifecycle := newLifecycle()
	lifecycle.Add(orderedLifecycleComponent{name: "first", events: &events, mu: &eventsMu})
	lifecycle.Add(orderedLifecycleComponent{name: "second", events: &events, mu: &eventsMu})
	lifecycle.Add(panickingInitializer{name: "panicking"})

	startDone := make(chan error, 1)
	go func() {
		startDone <- lifecycle.Start(context.Background())
	}()

	var startErr error
	select {
	case startErr = <-startDone:
	case <-time.After(time.Second):
		t.Fatal("Start() remained blocked after initializer panic")
	}
	if startErr == nil {
		t.Fatal("Start() error = nil, want initializer panic error")
	}
	if got := fmt.Sprint(startErr); !strings.Contains(got, "panic") {
		t.Fatalf("Start() error = %q, want lifecycle initializer panic", got)
	}

	wantEvents := []string{"init first", "init second", "stop second", "stop first"}
	eventsMu.Lock()
	gotEvents := append([]string(nil), events...)
	eventsMu.Unlock()
	if fmt.Sprint(gotEvents) != fmt.Sprint(wantEvents) {
		t.Fatalf("lifecycle events = %v, want %v", gotEvents, wantEvents)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := lifecycle.Start(ctx); err != startErr {
		t.Fatalf("repeated Start() error = %v, want cached %v", err, startErr)
	}
	if err := lifecycle.Stop(ctx); err != nil {
		t.Fatalf("Stop() after rollback error = %v", err)
	}
}

func TestStopWithCancelledContextDoesNotStartLegacyShutdown(t *testing.T) {
	component := &legacyBlockingShutdowner{
		name:    "legacy",
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	defer close(component.release)
	lifecycle := newLifecycle()
	lifecycle.Add(component)
	if err := lifecycle.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := lifecycle.Stop(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Stop() error = %v, want context.Canceled", err)
	}
	select {
	case <-component.started:
		t.Fatal("legacy shutdown started with an already-cancelled context")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestStopDeadlineDoesNotStartRemainingLIFOComponents(t *testing.T) {
	remaining := &legacyBlockingShutdowner{
		name:    "remaining",
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	close(remaining.release)
	blocking := &legacyBlockingShutdowner{
		name:    "blocking",
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	defer close(blocking.release)
	lifecycle := newLifecycle()
	lifecycle.Add(remaining)
	lifecycle.Add(blocking)
	if err := lifecycle.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := lifecycle.Stop(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Stop() error = %v, want context deadline", err)
	}
	if got := blocking.calls.Load(); got != 1 {
		t.Fatalf("blocking shutdown calls = %d, want 1", got)
	}
	select {
	case <-remaining.started:
		t.Fatal("remaining LIFO component started after shutdown deadline")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestLifecycleOperationWaitersRespectOwnContext(t *testing.T) {
	t.Run("Start", func(t *testing.T) {
		component := &gatedInitializer{
			name:    "blocking-start",
			entered: make(chan struct{}),
			release: make(chan struct{}),
		}
		lifecycle := newLifecycle()
		lifecycle.Add(component)
		firstDone := make(chan error, 1)
		go func() { firstDone <- lifecycle.Start(context.Background()) }()
		<-component.entered

		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		if err := lifecycle.Start(ctx); !errors.Is(err, context.DeadlineExceeded) {
			close(component.release)
			t.Fatalf("waiting Start() error = %v, want context deadline", err)
		}
		close(component.release)
		if err := <-firstDone; err != nil {
			t.Fatal(err)
		}
	})

	t.Run("Stop", func(t *testing.T) {
		component := &blockingContextShutdowner{
			name:    "blocking-stop",
			started: make(chan struct{}),
			release: make(chan struct{}),
		}
		lifecycle := newLifecycle()
		lifecycle.Add(component)
		if err := lifecycle.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		firstDone := make(chan error, 1)
		go func() { firstDone <- lifecycle.Stop(context.Background()) }()
		<-component.started

		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		if err := lifecycle.Stop(ctx); !errors.Is(err, context.DeadlineExceeded) {
			close(component.release)
			t.Fatalf("waiting Stop() error = %v, want context deadline", err)
		}
		close(component.release)
		if err := <-firstDone; err != nil {
			t.Fatal(err)
		}
	})
}

func TestDirectLaunchClosesPluginRegistration(t *testing.T) {
	config := NewSysConfig()
	config.Server.Port = int32(availableTCPPort(t))
	app := Ignite(config)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	launchDone := make(chan error, 1)
	go func() { launchDone <- app.Launch(ctx) }()
	waitForPluginBarrierClosed(t, app.pluginBarrier)

	for name, load := range map[string]func(string) error{
		"load":   app.LoadPlugin,
		"reload": app.ReloadPlugin,
	} {
		if err := load("/tmp/missing-plugin.so"); !errors.Is(err, ErrPluginHotReloadUnsupported) {
			cancel()
			<-launchDone
			t.Fatalf("%s during Launch error = %v, want ErrPluginHotReloadUnsupported", name, err)
		}
	}
	bean := &countedLifecycleBean{name: "late-direct-launch-plugin"}
	if err := app.pluginManager.registerModule(&lifecyclePluginModule{bean: bean}); !errors.Is(err, ErrPluginHotReloadUnsupported) {
		cancel()
		<-launchDone
		t.Fatalf("registerModule() during Launch error = %v, want ErrPluginHotReloadUnsupported", err)
	}
	if got := Resolve[*countedLifecycleBean](app.Runtime().Container); got != nil {
		cancel()
		<-launchDone
		t.Fatalf("late plugin bean entered container: %p", got)
	}

	cancel()
	if err := <-launchDone; err != nil {
		t.Fatalf("Launch() error = %v", err)
	}
}

func TestDirectLaunchSealsLifecycleRegistration(t *testing.T) {
	config := NewSysConfig()
	config.Server.Port = int32(availableTCPPort(t))
	app := Ignite(config)
	ctx, cancel := context.WithCancel(context.Background())
	launchDone := make(chan error, 1)
	go func() { launchDone <- app.Launch(ctx) }()
	waitForPluginBarrierClosed(t, app.pluginBarrier)

	deadline := time.Now().Add(time.Second)
	for !app.Runtime().Lifecycle.registrationClosed() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !app.Runtime().Lifecycle.registrationClosed() {
		cancel()
		<-launchDone
		t.Fatal("direct Launch did not close lifecycle registration")
	}

	late := &countedLifecycleComponent{name: "late-direct-launch"}
	if err := app.Runtime().Lifecycle.add(late); !errors.Is(err, ErrLifecycleRegistrationClosed) {
		cancel()
		<-launchDone
		t.Fatalf("lifecycle registration after direct Launch error = %v, want closed", err)
	}
	var hookCalls atomic.Int32
	if err := app.TryOnShutdown(func() { hookCalls.Add(1) }); !errors.Is(err, ErrLifecycleRegistrationClosed) {
		cancel()
		<-launchDone
		t.Fatalf("shutdown registration after direct Launch error = %v, want closed", err)
	}

	cancel()
	if err := <-launchDone; err != nil {
		t.Fatalf("Launch() error = %v", err)
	}
	if late.starts.Load() != 0 || late.stops.Load() != 0 || hookCalls.Load() != 0 {
		t.Fatalf("rejected registrations ran: starts=%d stops=%d hooks=%d", late.starts.Load(), late.stops.Load(), hookCalls.Load())
	}
}

func TestDirectLaunchLetsInFlightPluginFinishLifecycleRegistration(t *testing.T) {
	config := NewSysConfig()
	config.Server.Port = int32(availableTCPPort(t))
	app := Ignite(config)
	module := &gatedShutdownHookModule{
		beansEntered: make(chan struct{}),
		releaseBeans: make(chan struct{}),
		hookErr:      make(chan error, 1),
	}
	registrationDone := make(chan error, 1)
	go func() { registrationDone <- app.pluginManager.registerModule(module) }()
	<-module.beansEntered

	ctx, cancel := context.WithCancel(context.Background())
	launchDone := make(chan error, 1)
	go func() { launchDone <- app.Launch(ctx) }()
	waitForPluginBarrierClosing(t, app.pluginBarrier)
	close(module.releaseBeans)

	if err := <-registrationDone; err != nil {
		cancel()
		<-launchDone
		t.Fatalf("in-flight plugin registration error = %v", err)
	}
	if err := <-module.hookErr; err != nil {
		cancel()
		<-launchDone
		t.Fatalf("in-flight plugin shutdown hook error = %v", err)
	}
	cancel()
	if err := <-launchDone; err != nil {
		t.Fatalf("Launch() error = %v", err)
	}
}

func waitForPluginBarrierClosed(t *testing.T, barrier *pluginRegistrationBarrier) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		barrier.mu.Lock()
		closed := barrier.blocked && barrier.transition == nil
		barrier.mu.Unlock()
		if closed {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("plugin registration barrier did not close")
}

func waitForPluginBarrierClosing(t *testing.T, barrier *pluginRegistrationBarrier) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		barrier.mu.Lock()
		closing := barrier.blocked && barrier.transition != nil
		barrier.mu.Unlock()
		if closing {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("plugin registration barrier did not begin closing")
}

func TestLifecycleRejectsRegistrationAfterStartupBegins(t *testing.T) {
	component := &gatedInitializer{
		name:    "starting",
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	lifecycle := newLifecycle()
	lifecycle.Add(component)
	startDone := make(chan error, 1)
	go func() { startDone <- lifecycle.Start(context.Background()) }()
	<-component.entered
	late := &countedLifecycleComponent{name: "late"}
	if err := lifecycle.TryAdd(late); !errors.Is(err, ErrLifecycleRegistrationClosed) {
		close(component.release)
		t.Fatalf("Add() while starting error = %v, want lifecycle registration closed", err)
	}
	close(component.release)
	if err := <-startDone; err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if late.starts.Load() != 0 || late.stops.Load() != 0 {
		t.Fatalf("late component starts/stops = %d/%d, want 0/0", late.starts.Load(), late.stops.Load())
	}

	app := Ignite(NewSysConfig())
	if err := app.ApplyAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	var hookCalls atomic.Int32
	if err := app.TryOnShutdown(func() { hookCalls.Add(1) }); !errors.Is(err, ErrLifecycleRegistrationClosed) {
		t.Fatalf("OnShutdown() after startup error = %v, want lifecycle registration closed", err)
	}
	if err := app.Runtime().Lifecycle.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := hookCalls.Load(); got != 0 {
		t.Fatalf("late hook calls = %d, want 0", got)
	}
}

func TestLegacyVoidRegistrationMethodsIgnoreClosedLifecycle(t *testing.T) {
	app := Ignite(NewSysConfig())
	if err := app.ApplyAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	late := &countedLifecycleComponent{name: "legacy-late"}
	var hookCalls atomic.Int32

	app.Runtime().Lifecycle.Add(late)
	app.OnShutdown(func() { hookCalls.Add(1) })

	if err := app.Runtime().Lifecycle.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if late.starts.Load() != 0 || late.stops.Load() != 0 || hookCalls.Load() != 0 {
		t.Fatalf("closed legacy registrations ran: starts=%d stops=%d hooks=%d", late.starts.Load(), late.stops.Load(), hookCalls.Load())
	}
}

func TestEnableTracingAfterRegistrationSealPublishesNothing(t *testing.T) {
	config := NewSysConfig()
	config.Tracing.Enabled = true
	config.Tracing.Exporter = "none"
	app := Ignite(config)
	if err := app.ApplyAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	beforeHandlers := len(app.Engine.Handlers)

	app.EnableTracing(context.Background())

	if app.Runtime().TracerProvider != nil {
		t.Fatal("EnableTracing published a provider after lifecycle registration closed")
	}
	if app.Runtime().TextMapPropagator != nil {
		t.Fatal("EnableTracing published a propagator after lifecycle registration closed")
	}
	if got := len(app.Engine.Handlers); got != beforeHandlers {
		t.Fatalf("middleware handlers = %d, want unchanged %d", got, beforeHandlers)
	}
	if app.tracingRegistered.Load() {
		t.Fatal("tracing marked registered after lifecycle registration closed")
	}
	if err := app.Runtime().Lifecycle.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestApplyAllCachedResultWinsOverCancelledContext(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		app := Ignite(NewSysConfig())
		if err := app.ApplyAll(context.Background()); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := app.ApplyAll(ctx); err != nil {
			t.Fatalf("cached ApplyAll() error = %v, want nil", err)
		}
	})

	t.Run("failure", func(t *testing.T) {
		initErr := errors.New("cached failure")
		component := &gatedFailingInitializer{
			name:    "failing",
			err:     initErr,
			entered: make(chan struct{}),
			release: make(chan struct{}),
		}
		close(component.release)
		app := Ignite(NewSysConfig()).Beans(component)
		firstErr := app.ApplyAll(context.Background())
		if !errors.Is(firstErr, initErr) {
			t.Fatalf("ApplyAll() error = %v, want initializer error", firstErr)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := app.ApplyAll(ctx); err != firstErr {
			t.Fatalf("cached ApplyAll() error = %v, want stable %v", err, firstErr)
		}
	})
}

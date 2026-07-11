package bear

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
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

func (c *countedLifecycleComponent) Name() string { return c.name }

func (c *countedLifecycleComponent) Init(context.Context) error {
	c.starts.Add(1)
	return nil
}

func (c *countedLifecycleComponent) Shutdown() error {
	c.stops.Add(1)
	return nil
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
	if err := lifecycle.Add(late); !errors.Is(err, ErrLifecycleRegistrationClosed) {
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
	if err := app.OnShutdown(func() { hookCalls.Add(1) }); !errors.Is(err, ErrLifecycleRegistrationClosed) {
		t.Fatalf("OnShutdown() after startup error = %v, want lifecycle registration closed", err)
	}
	if err := app.Runtime().Lifecycle.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := hookCalls.Load(); got != 0 {
		t.Fatalf("late hook calls = %d, want 0", got)
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

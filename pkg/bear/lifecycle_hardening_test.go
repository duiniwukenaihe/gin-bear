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

type gatedFailingInitializer struct {
	name    string
	err     error
	entered chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (c *gatedFailingInitializer) Name() string { return c.name }

func (c *gatedFailingInitializer) Init(context.Context) error {
	if c.calls.Add(1) == 1 {
		close(c.entered)
	}
	<-c.release
	return c.err
}

type blockingContextShutdowner struct {
	name    string
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

type legacyBlockingShutdowner struct {
	name    string
	started chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (c *legacyBlockingShutdowner) Name() string { return c.name }

func (c *legacyBlockingShutdowner) Shutdown() error {
	if c.calls.Add(1) == 1 {
		close(c.started)
	}
	<-c.release
	return nil
}

type countedErrorShutdowner struct {
	name    string
	err     error
	started chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (c *countedErrorShutdowner) Name() string { return c.name }

func (c *countedErrorShutdowner) ShutdownContext(context.Context) error {
	if c.calls.Add(1) == 1 {
		close(c.started)
	}
	<-c.release
	return c.err
}

func (c *blockingContextShutdowner) Name() string { return c.name }

func (c *blockingContextShutdowner) ShutdownContext(context.Context) error {
	c.once.Do(func() { close(c.started) })
	<-c.release
	return nil
}

func TestProductionRejectsPlaceholderJWTSecrets(t *testing.T) {
	t.Setenv("BEAR_ENV", "prod")
	for _, secret := range []string{
		"replace-with-at-least-32-random-characters",
		"replace-with-at-least-64-random-characters",
		"replace_with_at_least_32_random_characters",
		"CHANGE-ME-to-a-random-production-secret",
	} {
		t.Run(secret, func(t *testing.T) {
			config := NewSysConfig()
			config.Auth.JWTSecret = secret
			if err := validateProductionSecurity(config); err == nil || !strings.Contains(err.Error(), "weak jwt secret") {
				t.Fatalf("validateProductionSecurity() error = %v, want placeholder rejection", err)
			}
		})
	}
}

func TestProductionRejectsUnsafeConfiguredTimeouts(t *testing.T) {
	t.Setenv("BEAR_ENV", "prod")
	tests := []struct {
		name string
		set  func(*SysConfig)
	}{
		{name: "read header zero", set: func(c *SysConfig) { c.Server.ReadHeaderTimeout = "0s" }},
		{name: "read negative", set: func(c *SysConfig) { c.Server.ReadTimeout = "-1s" }},
		{name: "write excessive", set: func(c *SysConfig) { c.Server.WriteTimeout = "24h" }},
		{name: "idle zero", set: func(c *SysConfig) { c.Server.IdleTimeout = "0s" }},
		{name: "shutdown excessive", set: func(c *SysConfig) { c.Server.ShutdownTimeout = "2h" }},
		{name: "readiness zero", set: func(c *SysConfig) { c.Health.ReadinessTimeout = "0s" }},
		{name: "slow request negative", set: func(c *SysConfig) { c.Middleware.SlowRequestThreshold = "-1ms" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := NewSysConfig()
			config.Auth.JWTSecret = "a-real-production-secret-with-more-than-32-characters"
			tt.set(config)
			if err := validateProductionSecurity(config); err == nil || !strings.Contains(err.Error(), "timeout") {
				t.Fatalf("validateProductionSecurity() error = %v, want timeout rejection", err)
			}
		})
	}
}

func TestApplyAllCachesFailureAndInitializesOnceConcurrently(t *testing.T) {
	initErr := errors.New("deterministic init failure")
	component := &gatedFailingInitializer{
		name:    "failing",
		err:     initErr,
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	app := Ignite(NewSysConfig()).Beans(component)

	const callers = 12
	results := make(chan error, callers)
	for range callers {
		go func() { results <- app.ApplyAll(context.Background()) }()
	}
	<-component.entered
	close(component.release)

	for range callers {
		if err := <-results; !errors.Is(err, initErr) {
			t.Fatalf("ApplyAll() error = %v, want cached initializer error", err)
		}
	}
	if got := component.calls.Load(); got != 1 {
		t.Fatalf("initializer calls = %d, want 1", got)
	}
	if err := app.ApplyAll(context.Background()); !errors.Is(err, initErr) {
		t.Fatalf("repeated ApplyAll() error = %v, want cached initializer error", err)
	}
}

func TestApplyAllRollsBackStartedComponentsInLIFOOrder(t *testing.T) {
	var events []string
	failing := &failingInitComponent{events: &events}
	app := Ignite(NewSysConfig())
	app.Runtime().Lifecycle.Add(recordingComponent{name: "first", events: &events})
	app.Runtime().Lifecycle.Add(recordingComponent{name: "second", events: &events})
	app.Beans(failing)

	if err := app.ApplyAll(context.Background()); err == nil {
		t.Fatal("ApplyAll() error = nil, want initializer failure")
	}
	assertStrings(t, events, []string{
		"start:first",
		"start:second",
		"start:failing-init",
		"stop:second",
		"stop:first",
	})
}

func TestApplyAllReportsInitializerRollbackFailureOnce(t *testing.T) {
	initErr := errors.New("initializer failed once")
	rollbackErr := errors.New("initializer rollback failed once")
	failing := &gatedFailingInitializer{
		name:    "failing-initializer",
		err:     initErr,
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	close(failing.release)
	app := Ignite(NewSysConfig())
	app.Runtime().Lifecycle.Add(failingShutdownComponent{name: "rollback-owner", err: rollbackErr})
	app.Beans(failing)

	err := app.ApplyAll(context.Background())
	if !errors.Is(err, initErr) || !errors.Is(err, rollbackErr) {
		t.Fatalf("ApplyAll() error = %v, want initializer and rollback failures", err)
	}
	if got := strings.Count(err.Error(), rollbackErr.Error()); got != 1 {
		t.Fatalf("rollback failure occurrences = %d, want 1 in %v", got, err)
	}
}

func TestLaunchRejectsCachedApplyFailure(t *testing.T) {
	initErr := errors.New("launch must stay closed")
	component := &gatedFailingInitializer{
		name:    "failing",
		err:     initErr,
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	close(component.release)
	app := Ignite(NewSysConfig()).Beans(component)
	if err := app.ApplyAll(context.Background()); !errors.Is(err, initErr) {
		t.Fatalf("ApplyAll() error = %v, want initializer error", err)
	}
	if err := app.Launch(context.Background()); !errors.Is(err, initErr) {
		t.Fatalf("Launch() error = %v, want cached initializer error", err)
	}
}

func TestShutdownContextHooksAreDeadlineBoundAndInstanceScoped(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	components := []*blockingContextShutdowner{
		{name: "first", started: make(chan struct{}), release: release},
		{name: "second", started: make(chan struct{}), release: release},
	}
	lifecycles := []*Lifecycle{newLifecycle(), newLifecycle()}
	for i := range lifecycles {
		lifecycles[i].Add(components[i])
		if err := lifecycles[i].Start(context.Background()); err != nil {
			t.Fatal(err)
		}
	}

	errs := make(chan error, len(lifecycles))
	for _, lifecycle := range lifecycles {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
			defer cancel()
			errs <- lifecycle.Stop(ctx)
		}()
	}
	for _, component := range components {
		select {
		case <-component.started:
		case <-time.After(200 * time.Millisecond):
			t.Fatal("one application shutdown blocked the other application hook")
		}
	}
	for range lifecycles {
		if err := <-errs; !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Stop() error = %v, want deadline exceeded", err)
		}
	}
}

func TestLegacyShutdownTimeoutIsCachedWithoutStartingAnotherWorker(t *testing.T) {
	component := &legacyBlockingShutdowner{
		name:    "legacy-blocking",
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	defer close(component.release)
	lifecycle := newLifecycle()
	lifecycle.Add(component)
	if err := lifecycle.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	firstErr := lifecycle.Stop(ctx)
	if !errors.Is(firstErr, context.DeadlineExceeded) {
		t.Fatalf("first Stop() error = %v, want deadline exceeded", firstErr)
	}

	const repeats = 12
	results := make(chan error, repeats)
	for range repeats {
		go func() { results <- lifecycle.Stop(context.Background()) }()
	}
	for range repeats {
		if err := <-results; err != firstErr {
			t.Fatalf("repeated Stop() error = %v, want cached %v", err, firstErr)
		}
	}
	if got := component.calls.Load(); got != 1 {
		t.Fatalf("legacy shutdown calls = %d, want one bounded worker", got)
	}
}

func TestConcurrentStopWaitersReceiveSameCachedHookError(t *testing.T) {
	hookErr := errors.New("shutdown failed")
	component := &countedErrorShutdowner{
		name:    "failing-shutdown",
		err:     hookErr,
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	lifecycle := newLifecycle()
	lifecycle.Add(component)
	if err := lifecycle.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	const callers = 16
	results := make(chan error, callers)
	for range callers {
		go func() { results <- lifecycle.Stop(context.Background()) }()
	}
	<-component.started
	close(component.release)
	var firstErr error
	for range callers {
		err := <-results
		if !errors.Is(err, hookErr) {
			t.Fatalf("Stop() error = %v, want hook error", err)
		}
		if firstErr == nil {
			firstErr = err
		} else if err != firstErr {
			t.Fatalf("Stop() returned unstable error instances: first=%p current=%p", firstErr, err)
		}
	}
	if got := component.calls.Load(); got != 1 {
		t.Fatalf("shutdown calls = %d, want 1", got)
	}
}

func TestProductionRejectsUnsafeWebSocketDynamicPolicy(t *testing.T) {
	t.Setenv("BEAR_ENV", "prod")
	tests := []struct {
		key   string
		value any
	}{
		{key: "websocket.max_message_bytes", value: "invalid"},
		{key: "websocket.max_message_bytes", value: 0},
		{key: "websocket.max_message_bytes", value: -1},
		{key: "websocket.max_message_bytes", value: int64(1 << 30)},
		{key: "websocket.read_timeout", value: "invalid"},
		{key: "websocket.read_timeout", value: "0s"},
		{key: "websocket.write_timeout", value: "-1s"},
		{key: "websocket.ping_interval", value: "24h"},
	}
	for _, tt := range tests {
		t.Run(tt.key+"/"+fmt.Sprint(tt.value), func(t *testing.T) {
			config := NewSysConfig()
			config.Auth.JWTSecret = "websocket-policy-test-secret-with-32-characters"
			config.Config = UserConfig{tt.key: tt.value}
			if err := validateProductionSecurity(config); err == nil || !strings.Contains(err.Error(), tt.key) {
				t.Fatalf("validateProductionSecurity() error = %v, want %s rejection", err, tt.key)
			}
		})
	}
}

func TestDevelopmentWebSocketDynamicPolicyKeepsSafeFallbacks(t *testing.T) {
	t.Setenv("BEAR_ENV", "dev")
	config := NewSysConfig()
	config.Config = UserConfig{
		"websocket.max_message_bytes": -1,
		"websocket.read_timeout":      "invalid",
		"websocket.write_timeout":     "0s",
		"websocket.ping_interval":     "-1s",
	}
	policy := webSocketPolicyForConfig(config)
	if policy.maxMessageBytes != defaultWebSocketMaxMessageBytes ||
		policy.readTimeout != defaultWebSocketReadTimeout ||
		policy.writeTimeout != defaultWebSocketWriteTimeout ||
		policy.pingInterval != defaultWebSocketPingInterval {
		t.Fatalf("development fallback policy = %+v", policy)
	}
}

func TestLoadConfigRejectsUnsafeProductionWebSocketDynamicPolicy(t *testing.T) {
	t.Setenv("BEAR_ENV", "prod")
	path := writeConfig(t, "application.yaml", `
auth:
  jwt_secret: websocket-load-test-secret-with-32-characters
config:
  websocket.read_timeout: 24h
`)
	_, err := LoadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "websocket.read_timeout") {
		t.Fatalf("LoadConfig() error = %v, want websocket policy rejection", err)
	}
}

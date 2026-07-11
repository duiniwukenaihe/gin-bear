package bear

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/robfig/cron/v3"
)

type lifecyclePluginModule struct {
	bean Bean
}

type countedCloser struct {
	closes atomic.Int32
}

func (c *countedCloser) Close() error {
	c.closes.Add(1)
	return nil
}

func (m *lifecyclePluginModule) Name() string  { return "lifecycle-plugin" }
func (m *lifecyclePluginModule) Beans() []Bean { return []Bean{m.bean} }
func (m *lifecyclePluginModule) Build(*Bear)   {}

type immediateOnceSchedule struct {
	fired bool
}

func (s *immediateOnceSchedule) Next(now time.Time) time.Time {
	if !s.fired {
		s.fired = true
		return now.Add(time.Millisecond)
	}
	return now.Add(time.Hour)
}

func TestCronShutdownContextPropagatesDeadline(t *testing.T) {
	manager := NewCronManager(nil)
	started := make(chan struct{})
	release := make(chan struct{})
	manager.scheduler.Schedule(&immediateOnceSchedule{}, cron.FuncJob(func() {
		close(started)
		<-release
	}))
	if err := manager.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("cron job did not start")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := manager.ShutdownContext(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		close(release)
		t.Fatalf("ShutdownContext() error = %v, want deadline exceeded", err)
	}
	close(release)
	if err := manager.ShutdownContext(context.Background()); err != nil {
		t.Fatalf("ShutdownContext() after job release error = %v", err)
	}
}

func TestMemoryRateLimiterShutdownWaitsForCleanupWorker(t *testing.T) {
	limiter := NewMemoryRateLimiter(1, time.Hour)
	if err := limiter.ShutdownContext(context.Background()); err != nil {
		t.Fatalf("ShutdownContext() error = %v", err)
	}
	select {
	case <-limiter.workerDone:
	default:
		t.Fatal("cleanup worker still running after ShutdownContext")
	}
	if err := limiter.Shutdown(); err != nil {
		t.Fatalf("repeated Shutdown() error = %v", err)
	}
}

func TestRedisRateLimiterImplementsUniformShutdown(t *testing.T) {
	limiter := NewRedisRateLimiter(nil, 1, time.Second)
	if err := limiter.ShutdownContext(context.Background()); err != nil {
		t.Fatalf("ShutdownContext() error = %v", err)
	}
	if err := limiter.Shutdown(); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
}

func TestPluginRejectsLateLifecycleBeansBeforeRegistration(t *testing.T) {
	app := Ignite(NewSysConfig())
	if err := app.ApplyAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	bean := &countedLifecycleBean{name: "late-plugin-resource"}
	err := app.pluginManager.registerModule(&lifecyclePluginModule{bean: bean})
	if err == nil {
		t.Fatal("registerModule() error = nil, want late lifecycle rejection")
	}
	if got := Resolve[*countedLifecycleBean](app.Runtime().Container); got != nil {
		t.Fatalf("late lifecycle bean was registered: %p", got)
	}
	if bean.starts != 0 || bean.stops != 0 {
		t.Fatalf("late lifecycle bean starts/stops = %d/%d, want 0/0", bean.starts, bean.stops)
	}
}

func TestWebSocketPolicyUsesSafeDefaultsAndRuntimeOverrides(t *testing.T) {
	defaults := webSocketPolicyForConfig(NewSysConfig())
	if defaults.maxMessageBytes <= 0 || defaults.readTimeout <= 0 || defaults.writeTimeout <= 0 || defaults.pingInterval <= 0 {
		t.Fatalf("unsafe websocket defaults: %+v", defaults)
	}

	config := NewSysConfig()
	config.Config = UserConfig{}
	config.Config["websocket.max_message_bytes"] = int64(2048)
	config.Config["websocket.read_timeout"] = "45s"
	config.Config["websocket.write_timeout"] = "7s"
	config.Config["websocket.ping_interval"] = "15s"
	policy := webSocketPolicyForConfig(config)
	if policy.maxMessageBytes != 2048 || policy.readTimeout != 45*time.Second || policy.writeTimeout != 7*time.Second || policy.pingInterval != 15*time.Second {
		t.Fatalf("websocket policy = %+v, want configured values", policy)
	}
}

func TestRuntimeClosesTrackedHijackedConnections(t *testing.T) {
	runtime := newRuntime(NewSysConfig())
	tracked := &countedCloser{}
	untracked := &countedCloser{}
	runtime.trackHijackedConnection(tracked)
	runtime.trackHijackedConnection(untracked)
	runtime.untrackHijackedConnection(untracked)

	if err := runtime.closeHijackedConnections(); err != nil {
		t.Fatalf("closeHijackedConnections() error = %v", err)
	}
	if got := tracked.closes.Load(); got != 1 {
		t.Fatalf("tracked close calls = %d, want 1", got)
	}
	if got := untracked.closes.Load(); got != 0 {
		t.Fatalf("untracked close calls = %d, want 0", got)
	}
	if err := runtime.closeHijackedConnections(); err != nil {
		t.Fatalf("repeated closeHijackedConnections() error = %v", err)
	}
	if got := tracked.closes.Load(); got != 1 {
		t.Fatalf("tracked close calls after repeat = %d, want 1", got)
	}
}

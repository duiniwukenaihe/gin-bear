package bear

import (
	"context"
	"errors"
	"sync"
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

type buildBeansPluginModule struct {
	bean  Bean
	built atomic.Bool
}

func (*buildBeansPluginModule) Name() string  { return "build-beans-plugin" }
func (*buildBeansPluginModule) Beans() []Bean { return nil }
func (m *buildBeansPluginModule) Build(app *Bear) {
	m.built.Store(true)
	app.Beans(m.bean)
}

type buildShutdownPluginModule struct {
	built     atomic.Bool
	hookCalls *atomic.Int32
}

type barrierPluginModule struct {
	beansEntered chan struct{}
	releaseBeans chan struct{}
	bean         Bean
	buildBean    Bean
	built        atomic.Bool
	hookCalls    *atomic.Int32
}

type buildLifecycleBean struct {
	*countedLifecycleBean
}

func (*buildShutdownPluginModule) Name() string  { return "build-shutdown-plugin" }
func (*buildShutdownPluginModule) Beans() []Bean { return nil }
func (m *buildShutdownPluginModule) Build(app *Bear) {
	m.built.Store(true)
	app.OnShutdown(func() { m.hookCalls.Add(1) })
}

func (*barrierPluginModule) Name() string { return "barrier-plugin" }
func (m *barrierPluginModule) Beans() []Bean {
	close(m.beansEntered)
	<-m.releaseBeans
	return []Bean{m.bean}
}
func (m *barrierPluginModule) Build(app *Bear) {
	m.built.Store(true)
	app.Beans(m.buildBean)
	app.OnShutdown(func() { m.hookCalls.Add(1) })
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

func TestPluginRejectsLateBuildBeforeBeansCanRegisterLifecycle(t *testing.T) {
	app := Ignite(NewSysConfig())
	if err := app.ApplyAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	bean := &countedLifecycleBean{name: "build-lifecycle-resource"}
	module := &buildBeansPluginModule{bean: bean}
	if err := app.pluginManager.registerModule(module); err == nil {
		t.Fatal("registerModule() error = nil, want all late plugins rejected")
	}
	if module.built.Load() {
		t.Fatal("late plugin Build ran before rejection")
	}
	if got := Resolve[*countedLifecycleBean](app.Runtime().Container); got != nil {
		t.Fatalf("Build registered late lifecycle bean: %p", got)
	}
}

func TestPluginRejectsLateBuildBeforeOnShutdownRegistration(t *testing.T) {
	app := Ignite(NewSysConfig())
	if err := app.ApplyAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	var hookCalls atomic.Int32
	module := &buildShutdownPluginModule{hookCalls: &hookCalls}
	if err := app.pluginManager.registerModule(module); err == nil {
		t.Fatal("registerModule() error = nil, want all late plugins rejected")
	}
	if module.built.Load() {
		t.Fatal("late plugin Build ran before rejection")
	}
	if err := app.Runtime().Lifecycle.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := hookCalls.Load(); got != 0 {
		t.Fatalf("late shutdown hook calls = %d, want 0", got)
	}
}

func TestPluginRejectsBuildAfterLifecycleWasStartedDirectly(t *testing.T) {
	app := Ignite(NewSysConfig())
	if err := app.Runtime().Lifecycle.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	bean := &countedLifecycleBean{name: "direct-start-resource"}
	module := &buildBeansPluginModule{bean: bean}
	if err := app.pluginManager.registerModule(module); err == nil {
		t.Fatal("registerModule() error = nil after direct lifecycle start")
	}
	if module.built.Load() {
		t.Fatal("plugin Build ran after direct lifecycle start")
	}
}

func TestPluginLoadAndReloadRejectHotReloadAfterApplyAll(t *testing.T) {
	app := Ignite(NewSysConfig())
	if err := app.ApplyAll(context.Background()); err != nil {
		t.Fatal(err)
	}

	for name, load := range map[string]func(string) error{
		"load":   app.LoadPlugin,
		"reload": app.ReloadPlugin,
	} {
		err := load("/tmp/missing-plugin.so")
		if !errors.Is(err, ErrPluginHotReloadUnsupported) {
			t.Fatalf("%s after ApplyAll error = %v, want ErrPluginHotReloadUnsupported", name, err)
		}
	}
}

func TestPluginRegistrationBarrierKeepsApplyAllBeforeModuleBuild(t *testing.T) {
	app := Ignite(NewSysConfig())
	beansBean := &countedLifecycleBean{name: "plugin-beans-resource"}
	buildBean := &buildLifecycleBean{countedLifecycleBean: &countedLifecycleBean{name: "plugin-build-resource"}}
	var hookCalls atomic.Int32
	module := &barrierPluginModule{
		beansEntered: make(chan struct{}),
		releaseBeans: make(chan struct{}),
		bean:         beansBean,
		buildBean:    buildBean,
		hookCalls:    &hookCalls,
	}
	registrationDone := make(chan error, 1)
	go func() {
		registrationDone <- app.pluginManager.registerModule(module)
	}()
	<-module.beansEntered
	defer func() {
		select {
		case <-module.releaseBeans:
		default:
			close(module.releaseBeans)
		}
		<-registrationDone
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if err := app.ApplyAll(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ApplyAll() during plugin registration error = %v, want registration barrier deadline", err)
	}
	close(module.releaseBeans)
	if err := <-registrationDone; err != nil {
		t.Fatalf("registerModule() error = %v", err)
	}
	registrationDone <- nil
	if !module.built.Load() {
		t.Fatal("plugin Build did not run after Beans was released")
	}

	if err := app.ApplyAll(context.Background()); err != nil {
		t.Fatalf("ApplyAll() after plugin registration error = %v", err)
	}
	if beansBean.starts != 1 || buildBean.starts != 1 {
		t.Fatalf("plugin bean starts = %d/%d, want 1/1", beansBean.starts, buildBean.starts)
	}
	if err := app.Runtime().Lifecycle.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if beansBean.stops != 1 || buildBean.stops != 1 || hookCalls.Load() != 1 {
		t.Fatalf("plugin shutdowns = %d/%d hook=%d, want 1/1/1", beansBean.stops, buildBean.stops, hookCalls.Load())
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

func TestRuntimeHijackedShutdownBarrierLeavesNoConnectionEscape(t *testing.T) {
	runtime := newRuntime(NewSysConfig())
	const connections = 64
	closers := make([]*countedCloser, connections)
	start := make(chan struct{})
	var ready sync.WaitGroup
	var done sync.WaitGroup
	ready.Add(connections)
	done.Add(connections)
	for i := range closers {
		closers[i] = &countedCloser{}
		connection := closers[i]
		go func() {
			defer done.Done()
			ready.Done()
			<-start
			runtime.trackHijackedConnection(connection)
		}()
	}
	ready.Wait()
	close(start)
	runtime.beginHijackedShutdown()
	done.Wait()
	if err := runtime.closeHijackedConnections(); err != nil {
		t.Fatal(err)
	}
	for i, connection := range closers {
		if got := connection.closes.Load(); got != 1 {
			t.Fatalf("connection %d close calls = %d, want exactly 1", i, got)
		}
	}
	late := &countedCloser{}
	if runtime.trackHijackedConnection(late) {
		t.Fatal("connection tracked after shutdown barrier")
	}
	if got := late.closes.Load(); got != 1 {
		t.Fatalf("late connection close calls = %d, want immediate close", got)
	}
}

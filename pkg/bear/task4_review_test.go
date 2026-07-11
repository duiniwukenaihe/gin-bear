package bear

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type runtimeResponseFairing struct {
	BaseFairing
	value string
}

type runtimePluginBean struct{}

func (*runtimePluginBean) Name() string { return "runtime-plugin-bean" }

type runtimePluginModule struct {
	bean *runtimePluginBean
}

func (*runtimePluginModule) Name() string { return "runtime-plugin" }
func (m *runtimePluginModule) Beans() []Bean {
	return []Bean{m.bean}
}
func (*runtimePluginModule) Build(*Bear) {}

type failingInitComponent struct {
	events *[]string
}

func (*failingInitComponent) Name() string { return "failing-init" }
func (c *failingInitComponent) Init(context.Context) error {
	*c.events = append(*c.events, "start:failing-init")
	return errors.New("init failed")
}
func (c *failingInitComponent) ShutdownContext(context.Context) error {
	*c.events = append(*c.events, "stop:failing-init")
	return nil
}

type aliasedLifecycle interface {
	Bean
	Initializer
	ContextShutdowner
}

type countedLifecycleBean struct {
	name   string
	starts int
	stops  int
}

type signalOrderHandler struct {
	registered        *atomic.Bool
	serveBeforeSignal *atomic.Bool
}

type messageCountHandler struct {
	message string
	count   *atomic.Int32
}

func (*signalOrderHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *signalOrderHandler) Handle(_ context.Context, record slog.Record) error {
	if record.Message == "WhiteBear is emerging from ice" && !h.registered.Load() {
		h.serveBeforeSignal.Store(true)
	}
	return nil
}
func (h *signalOrderHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *signalOrderHandler) WithGroup(string) slog.Handler      { return h }

func (*messageCountHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *messageCountHandler) Handle(_ context.Context, record slog.Record) error {
	if record.Message == h.message {
		h.count.Add(1)
	}
	return nil
}
func (h *messageCountHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *messageCountHandler) WithGroup(string) slog.Handler      { return h }

func (b *countedLifecycleBean) Name() string { return b.name }
func (b *countedLifecycleBean) Init(context.Context) error {
	b.starts++
	return nil
}
func (b *countedLifecycleBean) ShutdownContext(context.Context) error {
	b.stops++
	return nil
}

func (f *runtimeResponseFairing) OnResponse(interface{}) (interface{}, error) {
	return f.value, nil
}

func TestDefaultRuntimeFacadeIsPublishedAsOneSnapshot(t *testing.T) {
	const publishers = 32
	publishDefaultRuntime(newRuntime(NewSysConfig()))
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(publishers)

	for i := 0; i < publishers; i++ {
		go func() {
			defer wg.Done()
			<-start
			Ignite(NewSysConfig())
		}()
	}

	close(start)
	for i := 0; i < 10_000; i++ {
		facade := loadDefaultFacade()
		if facade == nil || facade.runtime == nil {
			continue
		}
		if facade.injector != facade.runtime.Container {
			t.Fatalf("facade injector %p does not belong to runtime %p", facade.injector, facade.runtime)
		}
		if facade.logger != facade.runtime.Logger {
			t.Fatalf("facade logger %p does not belong to runtime %p", facade.logger, facade.runtime)
		}
	}
	wg.Wait()
}

func TestGetInjectorBootstrapCannotOverwriteConcurrentIgnite(t *testing.T) {
	defaultFacade.Store(nil)
	bootstrapRead := make(chan struct{})
	releaseBootstrap := make(chan struct{})
	result := make(chan *BeanFactory, 1)

	go func() {
		result <- getInjector(func() {
			close(bootstrapRead)
			<-releaseBootstrap
		})
	}()

	<-bootstrapRead
	runtime := newRuntime(NewSysConfig())
	publishDefaultRuntime(runtime)
	close(releaseBootstrap)

	if got := <-result; got != runtime.Container {
		t.Fatalf("GetInjector() = %p, want concurrently published runtime container %p", got, runtime.Container)
	}
	facade := loadDefaultFacade()
	if facade == nil || facade.runtime != runtime || facade.injector != runtime.Container || facade.logger != runtime.Logger {
		t.Fatalf("facade = %#v, want coherent concurrently published runtime", facade)
	}
}

func TestGetInjectorPreservesLoggerOnlyLegacyFacade(t *testing.T) {
	logger := slog.New(&messageCountHandler{message: "unused", count: &atomic.Int32{}})
	defaultFacade.Store(&legacyFacade{logger: logger})
	result := make(chan *BeanFactory, 1)
	go func() { result <- GetInjector() }()

	select {
	case got := <-result:
		if got != bootstrapInjector {
			t.Fatalf("GetInjector() = %p, want bootstrap injector %p", got, bootstrapInjector)
		}
		facade := loadDefaultFacade()
		if facade == nil || facade.injector != bootstrapInjector || facade.logger != logger {
			t.Fatalf("facade = %#v, want logger preserved with bootstrap injector", facade)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("GetInjector blocked with a logger-only legacy facade")
	}
}

func TestResponderFairingsUseOwningRuntime(t *testing.T) {
	a := Ignite(NewSysConfig())
	a.Attach(&runtimeResponseFairing{value: "runtime-a"})
	a.Handle(http.MethodGet, "/value", func() (string, error) { return "raw", nil })

	b := Ignite(NewSysConfig())
	b.Attach(&runtimeResponseFairing{value: "runtime-b"})

	response := httptest.NewRecorder()
	a.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/value", nil))
	if response.Code != http.StatusOK || strings.TrimSpace(response.Body.String()) != `"runtime-a"` {
		t.Fatalf("app a response = %d %s, want its own response fairing", response.Code, response.Body.String())
	}
}

func TestAuthFairingUsesOwningRuntimePublicPaths(t *testing.T) {
	aConfig := NewSysConfig()
	aConfig.Auth.PublicPaths = []string{"/a-only/*"}
	a := Ignite(aConfig)
	a.Attach(NewAuthFairing())
	a.Handle(http.MethodGet, "/a-only/ping", func() (string, error) { return "public-a", nil })
	a.Handle(http.MethodGet, "/shared", func() (string, error) { return "private-a", nil })

	bConfig := NewSysConfig()
	bConfig.Auth.PublicPaths = []string{"/shared"}
	Ignite(bConfig)

	publicResponse := httptest.NewRecorder()
	a.ServeHTTP(publicResponse, httptest.NewRequest(http.MethodGet, "/a-only/ping", nil))
	if publicResponse.Code != http.StatusOK {
		t.Fatalf("app a public status = %d body = %s, want app a public policy", publicResponse.Code, publicResponse.Body.String())
	}

	response := httptest.NewRecorder()
	a.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/shared", nil))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("app a status = %d body = %s, want authentication rejection from app a policy", response.Code, response.Body.String())
	}
}

func TestResponderErrorsUseOwningRuntimeLogger(t *testing.T) {
	var aCount atomic.Int32
	var bCount atomic.Int32
	a := Ignite(NewSysConfig())
	a.runtime.Logger = slog.New(&messageCountHandler{message: "Handler execution failed", count: &aCount})
	a.Handle(http.MethodGet, "/fails", func() (string, error) {
		return "", errors.New("failed")
	})

	b := Ignite(NewSysConfig())
	b.runtime.Logger = slog.New(&messageCountHandler{message: "Handler execution failed", count: &bCount})
	publishDefaultRuntime(b.runtime)

	response := httptest.NewRecorder()
	a.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/fails", nil))
	if got := aCount.Load(); got != 1 {
		t.Fatalf("app a handler error logs = %d, want 1", got)
	}
	if got := bCount.Load(); got != 0 {
		t.Fatalf("app b handler error logs = %d, want 0", got)
	}
}

func TestGenerateOpenAPIUsesOwningRuntime(t *testing.T) {
	aConfig := NewSysConfig()
	aConfig.Server.Name = "runtime-a"
	a := Ignite(aConfig)

	bConfig := NewSysConfig()
	bConfig.Server.Name = "runtime-b"
	Ignite(bConfig)

	document, err := a.GenerateOpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	var schema OpenAPISchema
	if err := json.Unmarshal(document, &schema); err != nil {
		t.Fatal(err)
	}
	if got := schema.Info["title"]; got != "runtime-a" {
		t.Fatalf("OpenAPI title = %v, want runtime-a", got)
	}
}

func TestPluginValidationUsesOwningRuntime(t *testing.T) {
	allowedDir := t.TempDir()
	aConfig := NewSysConfig()
	aConfig.Plugins.Enabled = true
	aConfig.Plugins.AllowedDirs = []string{allowedDir}
	a := Ignite(aConfig)

	bConfig := NewSysConfig()
	bConfig.Plugins.Enabled = false
	Ignite(bConfig)

	err := a.LoadPlugin(filepath.Join(allowedDir, "missing.so"))
	if err == nil || strings.Contains(err.Error(), "disabled") {
		t.Fatalf("LoadPlugin() error = %v, want owning runtime policy followed through plugin open", err)
	}
}

func TestPluginModuleRegistrationUsesOwningRuntime(t *testing.T) {
	a := Ignite(NewSysConfig())
	b := Ignite(NewSysConfig())
	bean := &runtimePluginBean{}

	a.pluginManager.registerModule(&runtimePluginModule{bean: bean})

	if got := Resolve[*runtimePluginBean](a.Runtime().Container); got != bean {
		t.Fatalf("app a plugin bean = %p, want %p", got, bean)
	}
	if got := Resolve[*runtimePluginBean](b.Runtime().Container); got != nil {
		t.Fatalf("app b unexpectedly received app a plugin bean %p", got)
	}
}

func TestLifecycleStopsOnlySuccessfullyStartedComponents(t *testing.T) {
	var events []string
	lifecycle := newLifecycle()
	lifecycle.Add(recordingComponent{name: "first", events: &events})
	lifecycle.Add(&failingInitComponent{events: &events})
	lifecycle.Add(recordingComponent{name: "never-started", events: &events})

	if err := lifecycle.Start(context.Background()); err == nil {
		t.Fatal("Start() error = nil, want initializer failure")
	}
	if err := lifecycle.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertStrings(t, events, []string{"start:first", "start:failing-init", "stop:first"})
}

func TestLifecycleRejectsStartAfterStop(t *testing.T) {
	lifecycle := newLifecycle()
	if err := lifecycle.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.Start(context.Background()); err == nil {
		t.Fatal("Start() after Stop() error = nil")
	}
}

func TestLifecycleStopsSuccessfullyStartedComponentOnce(t *testing.T) {
	component := &countedLifecycleBean{name: "once"}
	lifecycle := newLifecycle()
	lifecycle.Add(component)
	if err := lifecycle.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if component.starts != 1 || component.stops != 1 {
		t.Fatalf("component starts/stops = %d/%d, want 1/1", component.starts, component.stops)
	}
}

func TestLifecycleDeduplicatesConcreteAndInterfaceRegistrations(t *testing.T) {
	runtime := newRuntime(NewSysConfig())
	component := &countedLifecycleBean{name: "aliased"}
	runtime.Container.Set(component)
	runtime.Container.SetWithInterface((*aliasedLifecycle)(nil), component)

	if err := runtime.Lifecycle.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Lifecycle.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if component.starts != 1 || component.stops != 1 {
		t.Fatalf("aliased component starts/stops = %d/%d, want 1/1", component.starts, component.stops)
	}
}

func TestLifecycleReplacementAfterStartStopsOldInstanceOnly(t *testing.T) {
	runtime := newRuntime(NewSysConfig())
	oldComponent := &countedLifecycleBean{name: "old"}
	replacement := &countedLifecycleBean{name: "replacement"}
	runtime.Container.Set(oldComponent)
	if err := runtime.Lifecycle.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	runtime.Container.Set(replacement)
	if err := runtime.Lifecycle.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if oldComponent.starts != 1 || oldComponent.stops != 1 {
		t.Fatalf("old component starts/stops = %d/%d, want 1/1", oldComponent.starts, oldComponent.stops)
	}
	if replacement.starts != 0 || replacement.stops != 0 {
		t.Fatalf("replacement starts/stops = %d/%d, want 0/0", replacement.starts, replacement.stops)
	}
}

func TestLifecycleRemovalAfterStartStillStopsStartedInstance(t *testing.T) {
	runtime := newRuntime(NewSysConfig())
	component := &countedLifecycleBean{name: "removed"}
	runtime.Container.Set(component)
	if err := runtime.Lifecycle.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	runtime.Container.Remove(reflect.TypeOf(component))
	if err := runtime.Lifecycle.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if component.starts != 1 || component.stops != 1 {
		t.Fatalf("removed component starts/stops = %d/%d, want 1/1", component.starts, component.stops)
	}
}

func TestBeanFactorySerializesMutationWithLifecycleTracking(t *testing.T) {
	lifecycle := newLifecycle()
	container := NewBeanFactory()
	setEntered := make(chan struct{})
	releaseSet := make(chan struct{})
	container.onSet = func(beanType reflect.Type, bean any) {
		close(setEntered)
		<-releaseSet
		lifecycle.setBean(beanType, bean)
	}
	container.onRemove = lifecycle.removeBean
	component := &countedLifecycleBean{name: "removed-during-set"}

	setDone := make(chan struct{})
	go func() {
		container.Set(component)
		close(setDone)
	}()
	<-setEntered

	removeDone := make(chan struct{})
	go func() {
		container.Remove(reflect.TypeOf(component))
		close(removeDone)
	}()
	select {
	case <-removeDone:
		close(releaseSet)
		<-setDone
		t.Fatal("Remove completed while Set lifecycle tracking was still pending")
	case <-time.After(20 * time.Millisecond):
	}

	close(releaseSet)
	<-setDone
	<-removeDone
	if err := lifecycle.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if component.starts != 0 || component.stops != 0 {
		t.Fatalf("removed component starts/stops = %d/%d, want 0/0", component.starts, component.stops)
	}
}

func TestShutdownPhasesHaveIndependentTimeouts(t *testing.T) {
	const timeout = 20 * time.Millisecond
	firstErr := runShutdownPhase(timeout, func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})
	if !errors.Is(firstErr, context.DeadlineExceeded) {
		t.Fatalf("first phase error = %v, want deadline exceeded", firstErr)
	}

	secondErr := runShutdownPhase(timeout, func(ctx context.Context) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return nil
	})
	if secondErr != nil {
		t.Fatalf("second phase inherited first deadline: %v", secondErr)
	}
}

func TestBlockingLegacyHooksLeaveBoundedWork(t *testing.T) {
	const hookCount = 12
	release := make(chan struct{})
	var started atomic.Int32
	lifecycles := make([]*Lifecycle, hookCount)
	for i := range lifecycles {
		lifecycle := newLifecycle()
		lifecycle.Add(shutdownHook{fn: func() {
			started.Add(1)
			<-release
		}})
		if err := lifecycle.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		lifecycles[i] = lifecycle
	}

	var wg sync.WaitGroup
	wg.Add(len(lifecycles))
	for _, lifecycle := range lifecycles {
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			_ = lifecycle.Stop(ctx)
		}()
	}
	wg.Wait()
	if got := started.Load(); got > 1 {
		close(release)
		t.Fatalf("started %d blocking legacy hooks, want at most one in-flight call", got)
	}
	close(release)
	deadline := time.Now().Add(time.Second)
	for len(legacyShutdownSlot) != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(legacyShutdownSlot) != 0 {
		t.Fatal("legacy shutdown slot was not released")
	}
}

func TestLaunchRegistersSignalContextBeforeServing(t *testing.T) {
	config := NewSysConfig()
	config.Server.Port = int32(availableTCPPort(t))
	app := Ignite(config)
	var registered atomic.Bool
	var serveBeforeSignal atomic.Bool
	app.runtime.Logger = slog.New(&signalOrderHandler{
		registered:        &registered,
		serveBeforeSignal: &serveBeforeSignal,
	})

	previous := signalNotifyContext
	signalNotifyContext = func(parent context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) {
		registered.Store(true)
		ctx, cancel := context.WithCancel(parent)
		cancel()
		return ctx, cancel
	}
	defer func() { signalNotifyContext = previous }()

	if err := app.Launch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if serveBeforeSignal.Load() {
		t.Fatal("HTTP serving began before signal context registration")
	}
}

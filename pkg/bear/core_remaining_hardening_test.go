package bear

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

type coreRemainingService interface {
	coreRemainingServiceMethod()
}

type coreRemainingServiceOne struct{}

func (*coreRemainingServiceOne) Name() string                { return "core-remaining-service-one" }
func (*coreRemainingServiceOne) coreRemainingServiceMethod() {}

type coreRemainingServiceTwo struct{}

func (*coreRemainingServiceTwo) Name() string                { return "core-remaining-service-two" }
func (*coreRemainingServiceTwo) coreRemainingServiceMethod() {}

type coreRemainingInjectionTarget struct {
	Service coreRemainingService `inject:"-"`
}

func TestCoreRemainingStrictInjectionReevaluatesChangedBeanGraph(t *testing.T) {
	resetGinModeForTest(t)
	config := NewSysConfig()
	config.SetFrameworkStrict(true)
	app := Ignite(config)
	if err := app.BeansE(&coreRemainingServiceOne{}); err != nil {
		t.Fatal(err)
	}
	target := &coreRemainingInjectionTarget{}
	if err := app.applyStrictObject(target); err != nil {
		t.Fatalf("first applyStrictObject() error = %v", err)
	}
	if err := app.BeansE(&coreRemainingServiceTwo{}); err != nil {
		t.Fatal(err)
	}

	err := app.applyStrictObject(target)
	if !errors.Is(err, ErrBeanAmbiguous) {
		t.Fatalf("second applyStrictObject() error = %v, want ErrBeanAmbiguous", err)
	}
}

type coreRemainingLifecycleBean struct {
	starts atomic.Int32
	stops  atomic.Int32
}

func (*coreRemainingLifecycleBean) Name() string { return "core-remaining-lifecycle" }
func (b *coreRemainingLifecycleBean) Init(context.Context) error {
	b.starts.Add(1)
	return nil
}
func (b *coreRemainingLifecycleBean) Shutdown() error {
	b.stops.Add(1)
	return nil
}

func TestCoreRemainingTryRemoveHonorsStrictLifecycleSeal(t *testing.T) {
	t.Run("before seal", func(t *testing.T) {
		resetGinModeForTest(t)
		config := NewSysConfig()
		config.SetFrameworkStrict(true)
		app := Ignite(config)
		bean := &coreRemainingLifecycleBean{}
		if err := app.BeansE(bean); err != nil {
			t.Fatal(err)
		}

		if err := app.Runtime().Container.TryRemove(reflect.TypeFor[*coreRemainingLifecycleBean]()); err != nil {
			t.Fatalf("TryRemove() error = %v", err)
		}
		if got := Resolve[*coreRemainingLifecycleBean](app.Runtime().Container); got != nil {
			t.Fatalf("removed bean still resolves: %p", got)
		}
		if err := app.ApplyAll(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := app.Runtime().Lifecycle.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}
		if bean.starts.Load() != 0 || bean.stops.Load() != 0 {
			t.Fatalf("removed bean starts/stops = %d/%d, want 0/0", bean.starts.Load(), bean.stops.Load())
		}
	})

	t.Run("after startup", func(t *testing.T) {
		resetGinModeForTest(t)
		config := NewSysConfig()
		config.SetFrameworkStrict(true)
		app := Ignite(config)
		bean := &coreRemainingLifecycleBean{}
		if err := app.BeansE(bean); err != nil {
			t.Fatal(err)
		}
		if err := app.ApplyAll(context.Background()); err != nil {
			t.Fatal(err)
		}
		beanType := reflect.TypeFor[*coreRemainingLifecycleBean]()

		if err := app.Runtime().Container.TryRemove(beanType); !errors.Is(err, ErrLifecycleRegistrationClosed) {
			t.Fatalf("TryRemove() error = %v, want ErrLifecycleRegistrationClosed", err)
		}
		app.Runtime().Container.Remove(beanType)
		if got := Resolve[*coreRemainingLifecycleBean](app.Runtime().Container); got != bean {
			t.Fatalf("closed removal changed resolved bean: %p, want %p", got, bean)
		}
		if err := app.Runtime().Lifecycle.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}
		if bean.starts.Load() != 1 || bean.stops.Load() != 1 {
			t.Fatalf("retained bean starts/stops = %d/%d, want 1/1", bean.starts.Load(), bean.stops.Load())
		}
	})
}

type coreRemainingFairing struct {
	BaseFairing
	id int
}

type coreRemainingLateFairing struct {
	BaseFairing
}

func TestCoreRemainingAttachERegistersBeforePublishing(t *testing.T) {
	resetGinModeForTest(t)
	config := NewSysConfig()
	config.SetFrameworkStrict(true)
	app := Ignite(config)
	first := &coreRemainingFairing{id: 1}
	if err := app.AttachE(first); err != nil {
		t.Fatalf("AttachE(first) error = %v", err)
	}
	if got := Resolve[*coreRemainingFairing](app.Runtime().Container); got != first {
		t.Fatalf("resolved fairing = %p, want %p", got, first)
	}
	beforePublished := len(app.fairingHandler.fairings)

	err := app.AttachE(&coreRemainingFairing{id: 2})
	if !errors.Is(err, ErrBeanDuplicate) {
		t.Fatalf("AttachE(duplicate) error = %v, want ErrBeanDuplicate", err)
	}
	if got := len(app.fairingHandler.fairings); got != beforePublished {
		t.Fatalf("failed AttachE published fairing count = %d, want %d", got, beforePublished)
	}

	if err := app.ApplyAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	late := &coreRemainingLateFairing{}
	beforeTargets := len(app.strictInjectionTargets)
	if err := app.AttachE(late); !errors.Is(err, ErrLifecycleRegistrationClosed) {
		t.Fatalf("late AttachE() error = %v, want ErrLifecycleRegistrationClosed", err)
	}
	if got := Resolve[*coreRemainingLateFairing](app.Runtime().Container); got != nil {
		t.Fatalf("late AttachE registered rejected fairing: %p", got)
	}
	if len(app.fairingHandler.fairings) != beforePublished || len(app.strictInjectionTargets) != beforeTargets {
		t.Fatal("late AttachE changed Fairing publication or injection targets")
	}
	assertCoreRemainingClosedPanic(t, func() { app.Attach(late) })
	if err := app.Runtime().Lifecycle.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func assertCoreRemainingClosedPanic(t *testing.T, action func()) {
	t.Helper()
	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatal("call did not panic")
		}
		err, ok := recovered.(error)
		if !ok || !errors.Is(err, ErrLifecycleRegistrationClosed) {
			t.Fatalf("panic = %v, want ErrLifecycleRegistrationClosed", recovered)
		}
	}()
	action()
}

type coreRemainingRouteFairing struct {
	BaseFairing
}

type coreRemainingWebSocketHandler struct {
	BaseWebSocketHandler
}

type coreRemainingController struct {
	builds atomic.Int32
}

func (*coreRemainingController) Name() string { return "core-remaining-controller" }
func (c *coreRemainingController) Build(app *Bear) {
	c.builds.Add(1)
	app.GET("/controller", func(*gin.Context) {})
}

func TestCoreRemainingStrictRouteEAPIsRejectAfterSealWithoutMutation(t *testing.T) {
	app := newCoreRemainingSealedStrictApp(t)
	controller := &coreRemainingController{}
	before := coreRemainingRegistrationSnapshotFor(app)
	handler := func(*gin.Context) {}

	tests := []struct {
		name string
		call func() error
	}{
		{name: "HandleE", call: func() error { return app.HandleE(http.MethodGet, "/handle-e", func() string { return "ok" }) }},
		{name: "HandleWithFairingE", call: func() error {
			return app.HandleWithFairingE(http.MethodGet, "/fairing-e", func() string { return "ok" }, &coreRemainingRouteFairing{})
		}},
		{name: "GroupE", call: func() error { _, err := app.GroupE("/group-e", controller); return err }},
		{name: "GETE", call: func() error { _, err := app.GETE("/get-e", handler); return err }},
		{name: "POSTE", call: func() error { _, err := app.POSTE("/post-e", handler); return err }},
		{name: "PUTE", call: func() error { _, err := app.PUTE("/put-e", handler); return err }},
		{name: "PATCHE", call: func() error { _, err := app.PATCHE("/patch-e", handler); return err }},
		{name: "DELETEE", call: func() error { _, err := app.DELETEE("/delete-e", handler); return err }},
		{name: "OPTIONSE", call: func() error { _, err := app.OPTIONSE("/options-e", handler); return err }},
		{name: "HEADE", call: func() error { _, err := app.HEADE("/head-e", handler); return err }},
		{name: "AnyE", call: func() error { _, err := app.AnyE("/any-e", handler); return err }},
		{name: "HandleWSE", call: func() error { return app.HandleWSE("/ws-e", &coreRemainingWebSocketHandler{}) }},
		{name: "UseE", call: func() error { return app.UseE(handler) }},
		{name: "EnableGzipE", call: func() error { return app.EnableGzipE(128) }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.call(); !errors.Is(err, ErrLifecycleRegistrationClosed) {
				t.Fatalf("error = %v, want ErrLifecycleRegistrationClosed", err)
			}
			assertCoreRemainingRegistrationSnapshot(t, app, before)
		})
	}
	if controller.builds.Load() != 0 {
		t.Fatalf("rejected GroupE built controller %d times", controller.builds.Load())
	}
}

func TestCoreRemainingStrictLegacyRouteAPIsPanicAfterSealWithoutMutation(t *testing.T) {
	app := newCoreRemainingSealedStrictApp(t)
	controller := &coreRemainingController{}
	before := coreRemainingRegistrationSnapshotFor(app)
	handler := func(*gin.Context) {}

	tests := []struct {
		name string
		call func()
	}{
		{name: "Handle", call: func() { app.Handle(http.MethodGet, "/handle", func() string { return "ok" }) }},
		{name: "HandleWithFairing", call: func() {
			app.HandleWithFairing(http.MethodGet, "/fairing", func() string { return "ok" }, &coreRemainingRouteFairing{})
		}},
		{name: "Group", call: func() { app.Group("/group", controller) }},
		{name: "GET", call: func() { app.GET("/get", handler) }},
		{name: "POST", call: func() { app.POST("/post", handler) }},
		{name: "PUT", call: func() { app.PUT("/put", handler) }},
		{name: "PATCH", call: func() { app.PATCH("/patch", handler) }},
		{name: "DELETE", call: func() { app.DELETE("/delete", handler) }},
		{name: "OPTIONS", call: func() { app.OPTIONS("/options", handler) }},
		{name: "HEAD", call: func() { app.HEAD("/head", handler) }},
		{name: "Any", call: func() { app.Any("/any", handler) }},
		{name: "HandleWS", call: func() { app.HandleWS("/ws", &coreRemainingWebSocketHandler{}) }},
		{name: "EnableGzip", call: func() { app.EnableGzip(128) }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertCoreRemainingClosedPanic(t, tt.call)
			assertCoreRemainingRegistrationSnapshot(t, app, before)
		})
	}
	if controller.builds.Load() != 0 {
		t.Fatalf("rejected Group built controller %d times", controller.builds.Load())
	}
}

type coreRemainingLateBean struct{}

func (*coreRemainingLateBean) Name() string { return "core-remaining-late-bean" }

type coreRemainingModule struct{}

func (*coreRemainingModule) Name() string  { return "core-remaining-module" }
func (*coreRemainingModule) Beans() []Bean { return nil }
func (*coreRemainingModule) Build(*Bear)   {}

func TestCoreRemainingStrictLegacyFluentAPIsPropagateRegistrationErrors(t *testing.T) {
	app := newCoreRemainingSealedStrictApp(t)
	beforeBeans := len(app.Runtime().Container.GetBeanMapper())
	beforeMetadata := len(app.exprData)
	beforeMounts := len(app.mounts)
	beforeModules := len(app.modules)

	assertCoreRemainingClosedPanic(t, func() { app.Beans(&coreRemainingLateBean{}) })
	assertCoreRemainingClosedPanic(t, func() { app.Mount("/late", &coreRemainingController{}) })
	assertCoreRemainingClosedPanic(t, func() { app.AddModule(&coreRemainingModule{}) })

	if got := len(app.Runtime().Container.GetBeanMapper()); got != beforeBeans {
		t.Fatalf("closed fluent API bean count = %d, want %d", got, beforeBeans)
	}
	if len(app.exprData) != beforeMetadata || len(app.mounts) != beforeMounts || len(app.modules) != beforeModules {
		t.Fatal("closed fluent API published metadata")
	}
}

type coreRemainingRegistrationSnapshot struct {
	routes     int
	handlers   int
	registry   int
	routeTrees int
	targets    int
	webSockets int64
}

func coreRemainingRegistrationSnapshotFor(app *Bear) coreRemainingRegistrationSnapshot {
	return coreRemainingRegistrationSnapshot{
		routes:     len(app.Routes()),
		handlers:   len(app.Engine.Handlers),
		registry:   len(app.routeRegistry),
		routeTrees: len(app.routeTree.trees),
		targets:    len(app.strictInjectionTargets),
		webSockets: app.webSocketRoutes.Load(),
	}
}

func assertCoreRemainingRegistrationSnapshot(t *testing.T, app *Bear, want coreRemainingRegistrationSnapshot) {
	t.Helper()
	if got := coreRemainingRegistrationSnapshotFor(app); got != want {
		t.Fatalf("registration state = %+v, want unchanged %+v", got, want)
	}
}

func newCoreRemainingSealedStrictApp(t *testing.T) *Bear {
	t.Helper()
	resetGinModeForTest(t)
	config := NewSysConfig()
	config.SetFrameworkStrict(true)
	app := Ignite(config)
	if err := app.ApplyAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := app.Runtime().Lifecycle.Stop(context.Background()); err != nil {
			t.Errorf("Lifecycle.Stop() error = %v", err)
		}
	})
	return app
}

func TestCoreRemainingIgniteEFailureDoesNotPublishRuntimeOrStrictGinMode(t *testing.T) {
	oldFacade := defaultFacade.Load()
	oldLogger := slog.Default()
	sentinel := &legacyFacade{runtime: &Runtime{}}
	defaultFacade.Store(sentinel)
	t.Cleanup(func() {
		defaultFacade.Store(oldFacade)
		slog.SetDefault(oldLogger)
	})

	ginRuntimeMu.Lock()
	oldStrictMode := strictGinRuntimeMode
	oldGinMode := gin.Mode()
	strictGinRuntimeMode = ""
	gin.SetMode(gin.DebugMode)
	ginRuntimeMu.Unlock()
	t.Cleanup(func() {
		ginRuntimeMu.Lock()
		strictGinRuntimeMode = oldStrictMode
		gin.SetMode(oldGinMode)
		ginRuntimeMu.Unlock()
	})

	config := NewSysConfig()
	config.SetFrameworkStrict(true)
	config.Server.TrustedProxies = []string{"not-a-proxy"}
	app, err := IgniteE(config)
	if err == nil || app != nil {
		t.Fatalf("IgniteE() = (%p, %v), want construction error", app, err)
	}
	if got := defaultFacade.Load(); got != sentinel {
		t.Fatalf("failed IgniteE published default facade %p, want sentinel %p", got, sentinel)
	}
	ginRuntimeMu.Lock()
	gotStrictMode := strictGinRuntimeMode
	ginRuntimeMu.Unlock()
	if gotStrictMode != "" {
		t.Fatalf("failed IgniteE committed strict Gin mode %q", gotStrictMode)
	}
}

func TestCoreRemainingEnableEAPIsRejectAfterSealWithoutPublication(t *testing.T) {
	resetGinModeForTest(t)
	config := NewSysConfig()
	config.SetFrameworkStrict(true)
	config.Tracing.Enabled = true
	config.Tracing.Exporter = "none"
	config.DB.Enabled = true
	config.DB.Type = "unsupported"
	config.DB.DSN = "configured-but-invalid"
	app := Ignite(config)
	if err := app.ApplyAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Runtime().Lifecycle.Stop(context.Background()) })
	before := coreRemainingRegistrationSnapshotFor(app)
	beforeMounts := len(app.mounts)
	beforeBeans := len(app.Runtime().Container.GetBeanMapper())

	tests := []struct {
		name string
		call func() error
	}{
		{name: "tracing", call: func() error { return app.EnableTracingE(context.Background()) }},
		{name: "metrics", call: app.EnableMetricsE},
		{name: "health", call: app.EnableHealthE},
		{name: "database", call: func() error { return app.EnableDatabaseE(context.Background()) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.call(); !errors.Is(err, ErrLifecycleRegistrationClosed) {
				t.Fatalf("error = %v, want ErrLifecycleRegistrationClosed", err)
			}
			assertCoreRemainingRegistrationSnapshot(t, app, before)
		})
	}
	if app.Runtime().TracerProvider != nil || app.Runtime().TextMapPropagator != nil || app.tracingRegistered.Load() {
		t.Fatal("rejected tracing enablement published runtime state")
	}
	if app.metricsRegistered.Load() || len(app.mounts) != beforeMounts || len(app.Runtime().Container.GetBeanMapper()) != beforeBeans {
		t.Fatal("rejected enablement published routes, mounts, or beans")
	}
}

func TestCoreRemainingEnableEAPIsRegisterWorkingResources(t *testing.T) {
	resetGinModeForTest(t)
	config := NewSysConfig()
	config.SetFrameworkStrict(true)
	config.Tracing.Enabled = true
	config.Tracing.Exporter = "none"
	config.DB.Enabled = true
	config.DB.Type = "sqlite"
	config.DB.DSN = filepath.Join(t.TempDir(), "core-remaining.db")
	config.Health.ReadinessTimeout = "1s"
	app := Ignite(config)

	if err := app.EnableTracingE(context.Background()); err != nil {
		t.Fatalf("EnableTracingE() error = %v", err)
	}
	if err := app.EnableHealthE(); err != nil {
		t.Fatalf("EnableHealthE() error = %v", err)
	}
	if err := app.EnableDatabaseE(context.Background()); err != nil {
		t.Fatalf("EnableDatabaseE() error = %v", err)
	}
	if Resolve[*GormAdapter](app.Runtime().Container) == nil {
		t.Fatal("EnableDatabaseE did not register GormAdapter")
	}
	if err := app.ApplyAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := app.Runtime().Lifecycle.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCoreRemainingIgniteEnablesConfiguredCORSWithoutAuthentication(t *testing.T) {
	resetGinModeForTest(t)
	config := NewSysConfig()
	config.CORS.Enabled = true
	config.CORS.AllowOrigins = []string{"https://client.example"}
	config.Auth.PublicPaths = nil
	app := Ignite(config)
	app.GET("/private", func(ctx *gin.Context) { ctx.String(http.StatusOK, "ok") })

	request := httptest.NewRequest(http.MethodGet, "/private", nil)
	request.Header.Set("Origin", "https://client.example")
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("unauthenticated response status = %d, want %d", response.Code, http.StatusOK)
	}
	if got := response.Header().Get("Access-Control-Allow-Origin"); got != "https://client.example" {
		t.Fatalf("automatic CORS header = %q, want configured origin", got)
	}
}

func TestCoreRemainingBuildFailureStopsPrestartedShutdownHook(t *testing.T) {
	resetGinModeForTest(t)
	config := NewSysConfig()
	config.SetFrameworkStrict(true)
	app := Ignite(config)
	var cleanups atomic.Int32
	if err := app.TryOnShutdown(func() { cleanups.Add(1) }); err != nil {
		t.Fatal(err)
	}
	buildErr := errors.New("prestarted cleanup build failure")
	if err := app.MountE("/api", &strictBuildErrorController{err: buildErr}); err != nil {
		t.Fatal(err)
	}
	if err := app.ApplyAll(context.Background()); !errors.Is(err, buildErr) {
		t.Fatalf("ApplyAll() error = %v, want build error", err)
	}
	if cleanups.Load() != 1 {
		t.Fatalf("prestarted cleanup calls = %d, want 1", cleanups.Load())
	}
	if err := app.Runtime().Lifecycle.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if cleanups.Load() != 1 {
		t.Fatalf("repeated Stop cleanup calls = %d, want 1", cleanups.Load())
	}
}

type coreRemainingPanickingInjectionTarget struct{}

func TestCoreRemainingInjectionPanicReleasesConcurrentWaiters(t *testing.T) {
	resetGinModeForTest(t)
	RegisterRuntimeStaticInjectorE(runtimeStaticInjectorKey(reflect.TypeFor[coreRemainingPanickingInjectionTarget]()), func(*BeanFactory, any) error {
		panic("injection detail")
	})
	config := NewSysConfig()
	config.SetFrameworkStrict(true)
	app := Ignite(config)
	target := &coreRemainingPanickingInjectionTarget{}

	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			results <- app.applyStrictObject(target)
		}()
	}
	close(start)
	done := make(chan struct{})
	go func() {
		workers.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("concurrent injection waiter remained blocked after panic")
	}
	close(results)
	for err := range results {
		if err == nil || !strings.Contains(err.Error(), "injection panic") || strings.Contains(err.Error(), "injection detail") {
			t.Fatalf("injection error = %v", err)
		}
	}
}

func TestCoreRemainingIgniteFailureRestoresGinGlobals(t *testing.T) {
	resetGinModeForTest(t)
	gin.SetMode(gin.TestMode)
	previousWriter := io.Discard
	previousErrorWriter := io.Discard
	gin.DefaultWriter = previousWriter
	gin.DefaultErrorWriter = previousErrorWriter
	config := NewSysConfig()
	config.Server.Mode = gin.ReleaseMode
	config.SetFrameworkStrict(true)
	config.Server.TrustedProxies = []string{"not a proxy"}

	if _, err := IgniteE(config); err == nil {
		t.Fatal("IgniteE() succeeded with invalid trusted proxy")
	}
	if gin.Mode() != gin.TestMode || gin.DefaultWriter != previousWriter || gin.DefaultErrorWriter != previousErrorWriter {
		t.Fatalf("Gin globals changed after failed IgniteE: mode=%s", gin.Mode())
	}
}

package bear

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

type strictBuildDependency struct {
	initialized atomic.Bool
}

func (*strictBuildDependency) Name() string { return "strict-build-dependency" }
func (d *strictBuildDependency) Init(context.Context) error {
	d.initialized.Store(true)
	return nil
}

type strictDiscoveredController struct {
	Dependency *strictBuildDependency `inject:"-"`
	buildCalls atomic.Int32
	initCalls  atomic.Int32
	events     *[]string
}

func (*strictDiscoveredController) Name() string { return "strict-discovered-controller" }
func (*strictDiscoveredController) Build(*Bear)  {}
func (c *strictDiscoveredController) BuildE(app *Bear) error {
	if c.Dependency == nil {
		return errors.New("controller dependency was not injected before BuildE")
	}
	c.buildCalls.Add(1)
	*c.events = append(*c.events, "controller-build")
	app.GET("/strict-discovered", func(ctx *gin.Context) { ctx.Status(204) })
	return nil
}
func (c *strictDiscoveredController) Init(context.Context) error {
	if c.Dependency == nil {
		return errors.New("controller dependency was not injected before Init")
	}
	c.initCalls.Add(1)
	*c.events = append(*c.events, "controller-init")
	return nil
}

type strictDiscoveringModule struct {
	dependency *strictBuildDependency
	controller *strictDiscoveredController
	buildCalls atomic.Int32
	events     *[]string
}

func (*strictDiscoveringModule) Name() string { return "strict-discovering-module" }
func (m *strictDiscoveringModule) Beans() []Bean {
	return []Bean{m.dependency}
}
func (*strictDiscoveringModule) Build(*Bear) {}
func (m *strictDiscoveringModule) BuildE(app *Bear) error {
	m.buildCalls.Add(1)
	*m.events = append(*m.events, "module-build")
	return app.MountE("/api", m.controller)
}

type strictBuildErrorModule struct {
	err      error
	panicErr error
}

func (*strictBuildErrorModule) Name() string  { return "strict-build-error-module" }
func (*strictBuildErrorModule) Beans() []Bean { return nil }
func (*strictBuildErrorModule) Build(*Bear)   {}
func (m *strictBuildErrorModule) BuildE(*Bear) error {
	if m.panicErr != nil {
		panic(m.panicErr)
	}
	return m.err
}

type strictBuildErrorController struct {
	err error
}

func (*strictBuildErrorController) Name() string { return "strict-build-error-controller" }
func (*strictBuildErrorController) Build(*Bear)  {}
func (c *strictBuildErrorController) BuildE(*Bear) error {
	return c.err
}

type strictBuildChainModule struct {
	index int
	limit int
}

func (m *strictBuildChainModule) Name() string { return fmt.Sprintf("strict-build-chain-%d", m.index) }
func (*strictBuildChainModule) Beans() []Bean  { return nil }
func (*strictBuildChainModule) Build(*Bear)    {}
func (m *strictBuildChainModule) BuildE(app *Bear) error {
	if m.index >= m.limit {
		return nil
	}
	return app.AddModuleE(&strictBuildChainModule{index: m.index + 1, limit: m.limit})
}

type strictGatedRegistrationModule struct {
	entered chan struct{}
	release chan struct{}
}

func (*strictGatedRegistrationModule) Name() string { return "strict-gated-registration-module" }
func (m *strictGatedRegistrationModule) Beans() []Bean {
	close(m.entered)
	<-m.release
	return nil
}
func (*strictGatedRegistrationModule) Build(*Bear) {}

type compatibilityBuildBean struct {
	events *[]string
}

func (*compatibilityBuildBean) Name() string { return "compatibility-build-bean" }
func (b *compatibilityBuildBean) Init(context.Context) error {
	*b.events = append(*b.events, "init")
	return nil
}

type compatibilityBuildModule struct {
	bean   *compatibilityBuildBean
	events *[]string
}

func (*compatibilityBuildModule) Name() string { return "compatibility-build-module" }
func (m *compatibilityBuildModule) Beans() []Bean {
	return []Bean{m.bean}
}
func (m *compatibilityBuildModule) Build(*Bear) {
	*m.events = append(*m.events, "build")
}

func strictBuildConfig() *SysConfig {
	config := NewSysConfig()
	config.SetFrameworkStrict(true)
	return config
}

func TestStrictBuildDiscoversInjectsAndInitializesBeforeServing(t *testing.T) {
	events := make([]string, 0, 3)
	dependency := &strictBuildDependency{}
	controller := &strictDiscoveredController{events: &events}
	module := &strictDiscoveringModule{
		dependency: dependency,
		controller: controller,
		events:     &events,
	}
	app := Ignite(strictBuildConfig())
	if err := app.AddModuleE(module); err != nil {
		t.Fatal(err)
	}

	if err := app.ApplyAll(context.Background()); err != nil {
		t.Fatalf("ApplyAll() error = %v", err)
	}
	if got := module.buildCalls.Load(); got != 1 {
		t.Fatalf("module BuildE calls = %d, want 1", got)
	}
	if got := controller.buildCalls.Load(); got != 1 {
		t.Fatalf("controller BuildE calls = %d, want 1", got)
	}
	if got := controller.initCalls.Load(); got != 1 {
		t.Fatalf("controller Init calls = %d, want 1", got)
	}
	if !dependency.initialized.Load() {
		t.Fatal("dependency was not initialized")
	}
	assertStrings(t, events, []string{"module-build", "controller-build", "controller-init"})

	if err := app.ApplyAll(context.Background()); err != nil {
		t.Fatalf("second ApplyAll() error = %v", err)
	}
	if module.buildCalls.Load() != 1 || controller.buildCalls.Load() != 1 {
		t.Fatalf("routes rebuilt: module=%d controller=%d", module.buildCalls.Load(), controller.buildCalls.Load())
	}
}

func TestBuildEReturnsModuleAndControllerErrors(t *testing.T) {
	t.Run("module", func(t *testing.T) {
		buildErr := errors.New("module build failed")
		app := Ignite(strictBuildConfig())
		if err := app.AddModuleE(&strictBuildErrorModule{err: buildErr}); err != nil {
			t.Fatal(err)
		}
		if err := app.ApplyAll(context.Background()); !errors.Is(err, buildErr) {
			t.Fatalf("ApplyAll() error = %v, want module BuildE error", err)
		}
	})

	t.Run("controller", func(t *testing.T) {
		buildErr := errors.New("controller build failed")
		app := Ignite(strictBuildConfig())
		if err := app.MountE("/api", &strictBuildErrorController{err: buildErr}); err != nil {
			t.Fatal(err)
		}
		if err := app.ApplyAll(context.Background()); !errors.Is(err, buildErr) {
			t.Fatalf("ApplyAll() error = %v, want controller BuildE error", err)
		}
	})

	t.Run("module panic preserves error", func(t *testing.T) {
		buildErr := errors.New("module build panicked")
		app := Ignite(strictBuildConfig())
		if err := app.AddModuleE(&strictBuildErrorModule{panicErr: buildErr}); err != nil {
			t.Fatal(err)
		}
		if err := app.ApplyAll(context.Background()); !errors.Is(err, buildErr) {
			t.Fatalf("ApplyAll() error = %v, want module panic error", err)
		}
	})

	t.Run("strict plugin", func(t *testing.T) {
		buildErr := errors.New("plugin module build failed")
		app := Ignite(strictBuildConfig())
		err := app.pluginManager.registerModule(&strictBuildErrorModule{err: buildErr})
		if !errors.Is(err, buildErr) {
			t.Fatalf("registerModule() error = %v, want BuildE error", err)
		}
		if err := app.ApplyAll(context.Background()); !errors.Is(err, buildErr) {
			t.Fatalf("ApplyAll() after failed plugin error = %v, want fail-closed BuildE error", err)
		}
	})
}

func TestStrictBuildRegistrationConvergenceLimit(t *testing.T) {
	t.Run("converges in 32 rounds", func(t *testing.T) {
		app := Ignite(strictBuildConfig())
		if err := app.AddModuleE(&strictBuildChainModule{index: 1, limit: 32}); err != nil {
			t.Fatal(err)
		}
		if err := app.ApplyAll(context.Background()); err != nil {
			t.Fatalf("ApplyAll() error = %v", err)
		}
	})

	t.Run("rejects round 33", func(t *testing.T) {
		app := Ignite(strictBuildConfig())
		if err := app.AddModuleE(&strictBuildChainModule{index: 1, limit: 33}); err != nil {
			t.Fatal(err)
		}
		err := app.ApplyAll(context.Background())
		if !errors.Is(err, ErrBuildRegistrationLoop) {
			t.Fatalf("ApplyAll() error = %v, want ErrBuildRegistrationLoop", err)
		}
	})
}

func TestCompatibilityBuildOrderRemainsInitBeforeBuild(t *testing.T) {
	events := make([]string, 0, 2)
	bean := &compatibilityBuildBean{events: &events}
	module := &compatibilityBuildModule{bean: bean, events: &events}
	app := Ignite(NewSysConfig()).AddModule(module)

	if err := app.ApplyAll(context.Background()); err != nil {
		t.Fatalf("ApplyAll() error = %v", err)
	}
	assertStrings(t, events, []string{"init", "build"})
}

func TestStrictBuildSealsAgainstInFlightERegistrationWithoutPartialPublish(t *testing.T) {
	app := Ignite(strictBuildConfig())
	module := &strictGatedRegistrationModule{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	registrationDone := make(chan error, 1)
	go func() { registrationDone <- app.AddModuleE(module) }()
	select {
	case <-module.entered:
	case <-time.After(time.Second):
		t.Fatal("AddModuleE did not enter Module.Beans")
	}

	applyDone := make(chan error, 1)
	go func() { applyDone <- app.ApplyAll(context.Background()) }()
	select {
	case err := <-applyDone:
		if err != nil {
			t.Fatalf("ApplyAll() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("ApplyAll blocked behind a registration that had not reached commit")
	}
	close(module.release)
	if err := <-registrationDone; !errors.Is(err, ErrLifecycleRegistrationClosed) {
		t.Fatalf("AddModuleE() error = %v, want ErrLifecycleRegistrationClosed", err)
	}
	if len(app.modules) != 0 {
		t.Fatalf("rejected in-flight module was published: %#v", app.modules)
	}
}

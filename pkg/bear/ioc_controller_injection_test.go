package bear

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

type controllerInjectedAuthFairing struct {
	BaseFairing
	JWTUtil *JWTUtil `inject:"-"`
}

func (f *controllerInjectedAuthFairing) OnRequest(ctx *gin.Context) error {
	if f.JWTUtil == nil {
		return NewError(http.StatusUnauthorized, "jwt util was not injected")
	}
	_, err := f.JWTUtil.ParseToken(ctx.GetHeader("Authorization")[len("Bearer "):])
	if err != nil {
		return NewError(http.StatusUnauthorized, "invalid token")
	}
	return nil
}

type controllerWithInjectedInterceptor struct {
	fairing Fairing
}

func (c *controllerWithInjectedInterceptor) Name() string { return "ControllerWithInjectedInterceptor" }

func (c *controllerWithInjectedInterceptor) Build(app *Bear) {
	app.Handle(http.MethodGet, "/private", func() string { return "ok" })
}

func (c *controllerWithInjectedInterceptor) Interceptors() []Fairing {
	return []Fairing{c.fairing}
}

func TestControllerInterceptorReceivesRuntimeContainerInjection(t *testing.T) {
	resetTestInjector()
	resetGinModeForTest(t)
	config := NewSysConfig()
	config.DB.Enabled = false
	config.Auth.JWTSecret = "controller-interceptor-injected-secret"
	config.Auth.PublicPaths = nil
	app := Ignite(config)
	interceptor := &controllerInjectedAuthFairing{}
	app.Mount("/api", &controllerWithInjectedInterceptor{fairing: interceptor})
	if err := app.ApplyAll(context.Background()); err != nil {
		t.Fatalf("ApplyAll() error = %v", err)
	}

	util := NewJWTUtil(config.Auth.JWTSecret, 1)
	token, err := util.GenerateToken(1, "user@example.com")
	if err != nil {
		t.Fatalf("GenerateToken() error = %v", err)
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/private", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	app.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusOK, response.Body.String())
	}
	if interceptor.JWTUtil == nil {
		t.Fatal("controller interceptor did not receive the runtime JWTUtil")
	}
}

type terminalControllerFairing struct {
	BaseFairing
}

func (f *terminalControllerFairing) OnRequest(ctx *gin.Context) error {
	ctx.String(http.StatusForbidden, "blocked")
	return nil
}

type terminalController struct {
	fairing Fairing
	called  *bool
}

func (c *terminalController) Name() string { return "terminal-controller" }

func (c *terminalController) Interceptors() []Fairing { return []Fairing{c.fairing} }

func (c *terminalController) Build(app *Bear) {
	app.GET("/direct", func(*gin.Context) { *c.called = true })
}

func TestControllerFairingTerminal(t *testing.T) {
	called := false
	app := Ignite(NewSysConfig())
	app.Mount("/api", &terminalController{
		fairing: &terminalControllerFairing{},
		called:  &called,
	})
	if err := app.ApplyAll(context.Background()); err != nil {
		t.Fatalf("ApplyAll() error = %v", err)
	}

	response := performRequest(app, httptest.NewRequest(http.MethodGet, "/api/direct", nil))
	if response.Code != http.StatusForbidden || response.Body.String() != "blocked" {
		t.Fatalf("response = %d %q", response.Code, response.Body.String())
	}
	if called {
		t.Fatal("direct controller handler ran after Fairing wrote a response")
	}
}

type iocControllerTestAlias interface {
	iocControllerTestMethod()
}

type iocControllerTestBean struct{}

func (*iocControllerTestBean) iocControllerTestMethod() {}

type iocControllerTestOtherBean struct{}

type runtimeScopedStaticTarget struct {
	Config *SysConfig
}

type legacyRuntimeScopedTarget struct {
	Config *SysConfig `inject:"-"`
}

func TestRuntimeStaticInjectorResolvesFromOwningContainer(t *testing.T) {
	staticMu.Lock()
	previous, hadPrevious := runtimeStaticInjectors["runtimeScopedStaticTarget"]
	staticMu.Unlock()
	t.Cleanup(func() {
		staticMu.Lock()
		if hadPrevious {
			runtimeStaticInjectors["runtimeScopedStaticTarget"] = previous
		} else {
			delete(runtimeStaticInjectors, "runtimeScopedStaticTarget")
		}
		staticMu.Unlock()
	})
	RegisterRuntimeStaticInjector("runtimeScopedStaticTarget", func(factory *BeanFactory, obj interface{}) {
		obj.(*runtimeScopedStaticTarget).Config = Resolve[*SysConfig](factory)
	})

	firstConfig := NewSysConfig()
	firstConfig.Server.Name = "first-runtime"
	first := Ignite(firstConfig)
	secondConfig := NewSysConfig()
	secondConfig.Server.Name = "second-runtime"
	Ignite(secondConfig)

	target := &runtimeScopedStaticTarget{}
	first.runtime.Container.Apply(target)
	if target.Config != firstConfig {
		t.Fatalf("runtime static injection used config %p (%q), want first runtime config %p", target.Config, target.Config.Server.Name, firstConfig)
	}
}

func TestLegacyStaticInjectorFallsBackToOwningContainer(t *testing.T) {
	staticMu.Lock()
	previous, hadPrevious := staticInjectors["legacyRuntimeScopedTarget"]
	staticMu.Unlock()
	t.Cleanup(func() {
		staticMu.Lock()
		if hadPrevious {
			staticInjectors["legacyRuntimeScopedTarget"] = previous
		} else {
			delete(staticInjectors, "legacyRuntimeScopedTarget")
		}
		staticMu.Unlock()
	})
	RegisterStaticInjector("legacyRuntimeScopedTarget", func(obj interface{}) {
		obj.(*legacyRuntimeScopedTarget).Config = GetByType[*SysConfig]()
	})

	firstConfig := NewSysConfig()
	firstConfig.Server.Name = "legacy-first-runtime"
	first := Ignite(firstConfig)
	secondConfig := NewSysConfig()
	secondConfig.Server.Name = "legacy-second-runtime"
	Ignite(secondConfig)

	target := &legacyRuntimeScopedTarget{}
	first.runtime.Container.Apply(target)
	if target.Config != firstConfig {
		t.Fatalf("legacy static injection used config %p, want first runtime config %p", target.Config, firstConfig)
	}
}

func TestTrySetWithInterfaceRejectsInvalidRegistrationWithoutMutation(t *testing.T) {
	tests := []struct {
		name     string
		ifacePtr any
		bean     any
	}{
		{name: "nil interface pointer", ifacePtr: nil, bean: &iocControllerTestBean{}},
		{name: "non-pointer", ifacePtr: "not an interface pointer", bean: &iocControllerTestBean{}},
		{name: "pointer to concrete type", ifacePtr: (*iocControllerTestBean)(nil), bean: &iocControllerTestBean{}},
		{name: "nil bean", ifacePtr: (*iocControllerTestAlias)(nil), bean: nil},
		{name: "bean does not implement interface", ifacePtr: (*iocControllerTestAlias)(nil), bean: &iocControllerTestOtherBean{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			factory := NewBeanFactory()
			if err := factory.TrySetWithInterface(tt.ifacePtr, tt.bean); err == nil {
				t.Fatal("TrySetWithInterface() error = nil, want rejection")
			}
			if got := Resolve[iocControllerTestAlias](factory); got != nil {
				t.Fatalf("invalid registration published bean %T", got)
			}
		})
	}
}

func TestTrySetWithInterfaceRegistersCompatibleBeanAndLegacyAPIStaysSafe(t *testing.T) {
	factory := NewBeanFactory()
	bean := &iocControllerTestBean{}
	if err := factory.TrySetWithInterface((*iocControllerTestAlias)(nil), bean); err != nil {
		t.Fatalf("TrySetWithInterface() error = %v", err)
	}
	if got := Resolve[iocControllerTestAlias](factory); got != bean {
		t.Fatalf("Resolve() = %T, want registered bean", got)
	}

	legacy := NewBeanFactory()
	legacy.SetWithInterface(nil, bean)
	if got := Resolve[iocControllerTestAlias](legacy); got != nil {
		t.Fatalf("legacy invalid registration published bean %T", got)
	}
	legacy.SetWithInterface((*iocControllerTestAlias)(nil), bean)
	if got := Resolve[iocControllerTestAlias](legacy); got != bean {
		t.Fatalf("legacy valid registration = %T, want registered bean", got)
	}
}

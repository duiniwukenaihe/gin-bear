package bear

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
)

func newPluginDispatcherRouter(dispatcher *PluginDispatcher) *gin.Engine {
	router := gin.New()
	router.NoRoute(dispatcher.Dispatch(), func(ctx *gin.Context) {
		ctx.String(http.StatusNotFound, "fallback")
	})
	return router
}

func newTestPluginDispatcher() *PluginDispatcher {
	return newPluginDispatcher(slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func dispatchPluginRequest(router http.Handler, method, path string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(method, path, nil))
	return response
}

func TestPluginDispatcherMatchesRoutesWithoutGinFullPath(t *testing.T) {
	dispatcher := newTestPluginDispatcher()
	dispatcher.Register(http.MethodGet, "/plugins/:name", func(ctx *gin.Context) {
		ctx.String(http.StatusOK, "dynamic:%s", ctx.Param("name"))
	})
	dispatcher.Register(http.MethodGet, "/plugins/status", func(ctx *gin.Context) {
		ctx.String(http.StatusOK, "static")
	})

	router := newPluginDispatcherRouter(dispatcher)
	for _, tt := range []struct {
		name       string
		method     string
		path       string
		wantStatus int
		wantBody   string
	}{
		{name: "parameter route", method: http.MethodGet, path: "/plugins/bear", wantStatus: http.StatusOK, wantBody: "dynamic:bear"},
		{name: "static wins over parameter route", method: http.MethodGet, path: "/plugins/status", wantStatus: http.StatusOK, wantBody: "static"},
		{name: "method is isolated", method: http.MethodPost, path: "/plugins/bear", wantStatus: http.StatusNotFound, wantBody: "fallback"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			response := dispatchPluginRequest(router, tt.method, tt.path)
			if response.Code != tt.wantStatus || response.Body.String() != tt.wantBody {
				t.Fatalf("response = %d %q, want %d %q", response.Code, response.Body.String(), tt.wantStatus, tt.wantBody)
			}
		})
	}
}

func TestPluginDispatcherMatchesCatchAllAndKeepsParamRoutePrecedenceStable(t *testing.T) {
	dispatcher := newTestPluginDispatcher()
	dispatcher.Register(http.MethodGet, "/items/:name", func(ctx *gin.Context) {
		ctx.String(http.StatusOK, "first:%s", ctx.Param("name"))
	})
	err := dispatcher.RegisterE(http.MethodGet, "/items/:id", func(ctx *gin.Context) {
		ctx.String(http.StatusOK, "second:%s", ctx.Param("id"))
	})
	if !errors.Is(err, ErrPluginRouteConflict) {
		t.Fatalf("RegisterE() error = %v, want ErrPluginRouteConflict", err)
	}
	dispatcher.Register(http.MethodGet, "/assets/*filepath", func(ctx *gin.Context) {
		ctx.String(http.StatusOK, "asset:%s", ctx.Param("filepath"))
	})

	router := newPluginDispatcherRouter(dispatcher)
	for _, tt := range []struct {
		path string
		want string
	}{
		{path: "/items/bear", want: "first:bear"},
		{path: "/assets/css/app.css", want: "asset:/css/app.css"},
	} {
		t.Run(tt.path, func(t *testing.T) {
			response := dispatchPluginRequest(router, http.MethodGet, tt.path)
			if response.Code != http.StatusOK || response.Body.String() != tt.want {
				t.Fatalf("response = %d %q, want %d %q", response.Code, response.Body.String(), http.StatusOK, tt.want)
			}
		})
	}
}

func TestPluginDispatcherPrefersMoreSpecificDynamicRoute(t *testing.T) {
	dispatcher := newTestPluginDispatcher()
	dispatcher.Register(http.MethodGet, "/tenants/:tenant/:name", func(ctx *gin.Context) {
		ctx.String(http.StatusOK, "parameter")
	})
	dispatcher.Register(http.MethodGet, "/tenants/:tenant/admin", func(ctx *gin.Context) {
		ctx.String(http.StatusOK, "admin")
	})
	dispatcher.Register(http.MethodGet, "/tenants/*path", func(ctx *gin.Context) {
		ctx.String(http.StatusOK, "catch-all")
	})

	router := newPluginDispatcherRouter(dispatcher)
	response := dispatchPluginRequest(router, http.MethodGet, "/tenants/acme/admin")
	if response.Code != http.StatusOK || response.Body.String() != "admin" {
		t.Fatalf("specific route response = %d %q, want %d %q", response.Code, response.Body.String(), http.StatusOK, "admin")
	}
	response = dispatchPluginRequest(router, http.MethodGet, "/tenants/acme/member")
	if response.Code != http.StatusOK || response.Body.String() != "parameter" {
		t.Fatalf("parameter route response = %d %q, want %d %q", response.Code, response.Body.String(), http.StatusOK, "parameter")
	}
}

type pluginControllerFairing struct {
	BaseFairing
	calls atomic.Int64
}

func (f *pluginControllerFairing) OnRequest(*gin.Context) error {
	f.calls.Add(1)
	return NewError(http.StatusForbidden, "plugin controller denied")
}

func TestPluginRouteExecutesControllerFairings(t *testing.T) {
	resetGinModeForTest(t)
	config := NewSysConfig()
	config.SetFrameworkStrict(true)
	app := Ignite(config)
	fairing := &pluginControllerFairing{}
	group := app.Engine.Group("/plugins")
	app.registration = &routeRegistrationContext{
		group:     group,
		groupName: group.BasePath(),
		fairings:  []Fairing{fairing},
	}
	app.pluginMode = true
	err := app.HandleE(http.MethodGet, "/private", func() string { return "secret" })
	app.pluginMode = false
	app.registration = nil
	if err != nil {
		t.Fatalf("HandleE() error = %v", err)
	}
	if err := app.ApplyAll(context.Background()); err != nil {
		t.Fatalf("ApplyAll() error = %v", err)
	}

	response := dispatchPluginRequest(app, http.MethodGet, "/plugins/private")
	if response.Code != http.StatusForbidden {
		t.Fatalf("plugin controller fairing status = %d, want %d; body=%s", response.Code, http.StatusForbidden, response.Body.String())
	}
	if calls := fairing.calls.Load(); calls != 1 {
		t.Fatalf("plugin controller fairing calls = %d, want 1", calls)
	}
}

func TestPluginDispatcherReplacesAndUnregistersRoutes(t *testing.T) {
	dispatcher := newTestPluginDispatcher()
	dispatcher.Register(http.MethodGet, "/plugins/:name", func(ctx *gin.Context) {
		ctx.String(http.StatusOK, "old:%s", ctx.Param("name"))
	})
	dispatcher.Register(http.MethodGet, "/plugins/:name", func(ctx *gin.Context) {
		ctx.String(http.StatusOK, "new:%s", ctx.Param("name"))
	})

	router := newPluginDispatcherRouter(dispatcher)
	response := dispatchPluginRequest(router, http.MethodGet, "/plugins/bear")
	if response.Code != http.StatusOK || response.Body.String() != "new:bear" {
		t.Fatalf("replacement response = %d %q", response.Code, response.Body.String())
	}

	dispatcher.Unregister(http.MethodGet, "/plugins/:name")
	response = dispatchPluginRequest(router, http.MethodGet, "/plugins/bear")
	if response.Code != http.StatusNotFound || response.Body.String() != "fallback" {
		t.Fatalf("unregistered response = %d %q", response.Code, response.Body.String())
	}
}

func TestPluginDispatcherAllowsConcurrentMutationAndDispatch(t *testing.T) {
	dispatcher := newTestPluginDispatcher()
	var reentrantCalls atomic.Int32
	dispatcher.Register(http.MethodGet, "/plugins/:name", func(ctx *gin.Context) {
		reentrantCalls.Add(1)
		dispatcher.Register(http.MethodGet, "/plugins/:name", func(ctx *gin.Context) {
			ctx.String(http.StatusOK, ctx.Param("name"))
		})
	})
	router := newPluginDispatcherRouter(dispatcher)

	var workers sync.WaitGroup
	for range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for range 100 {
				dispatchPluginRequest(router, http.MethodGet, "/plugins/bear")
				dispatcher.Unregister(http.MethodGet, "/plugins/:name")
				dispatcher.Register(http.MethodGet, "/plugins/:name", func(ctx *gin.Context) {
					ctx.String(http.StatusOK, ctx.Param("name"))
				})
			}
		}()
	}
	workers.Wait()
	if reentrantCalls.Load() == 0 {
		t.Fatal("expected a dispatched handler to reenter Register")
	}
}

func TestPluginRoutesUseFullGroupPathAndCallerMiddleware(t *testing.T) {
	resetGinModeForTest(t)
	config := NewSysConfig()
	config.SetFrameworkStrict(true)
	var middlewareCalls atomic.Int64
	app, err := IgniteE(config, gin.HandlerFunc(func(ctx *gin.Context) {
		middlewareCalls.Add(1)
		ctx.Next()
	}))
	if err != nil {
		t.Fatalf("IgniteE() error = %v", err)
	}

	group := app.Engine.Group("/plugins")
	app.registration = &routeRegistrationContext{group: group, groupName: group.BasePath()}
	app.pluginMode = true
	err = app.HandleE(http.MethodGet, "/:name", func(ctx *gin.Context) {
		ctx.String(http.StatusOK, "plugin:%s", ctx.Param("name"))
	})
	app.pluginMode = false
	app.registration = nil
	if err != nil {
		t.Fatalf("HandleE() error = %v", err)
	}
	if err := app.ApplyAll(context.Background()); err != nil {
		t.Fatalf("ApplyAll() error = %v", err)
	}

	response := dispatchPluginRequest(app, http.MethodGet, "/plugins/bear")
	if response.Code != http.StatusOK || response.Body.String() != "plugin:bear" {
		t.Fatalf("plugin response = %d %q, want %d %q", response.Code, response.Body.String(), http.StatusOK, "plugin:bear")
	}
	if calls := middlewareCalls.Load(); calls != 1 {
		t.Fatalf("caller middleware calls = %d, want 1", calls)
	}
}

func TestPluginRoutesUseMiddlewareRegisteredAfterIgnite(t *testing.T) {
	resetGinModeForTest(t)
	config := NewSysConfig()
	config.SetFrameworkStrict(true)
	app := Ignite(config)
	var middlewareCalls atomic.Int64
	if err := app.UseE(func(ctx *gin.Context) {
		middlewareCalls.Add(1)
		ctx.Next()
	}); err != nil {
		t.Fatalf("UseE() error = %v", err)
	}

	app.pluginMode = true
	err := app.HandleE(http.MethodGet, "/plugin-late-middleware", func() string { return "ok" })
	app.pluginMode = false
	if err != nil {
		t.Fatalf("HandleE() error = %v", err)
	}
	if err := app.ApplyAll(context.Background()); err != nil {
		t.Fatalf("ApplyAll() error = %v", err)
	}
	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })

	response := dispatchPluginRequest(app, http.MethodGet, "/plugin-late-middleware")
	if response.Code != http.StatusOK {
		t.Fatalf("plugin response = %d %q, want %d", response.Code, response.Body.String(), http.StatusOK)
	}
	if calls := middlewareCalls.Load(); calls != 1 {
		t.Fatalf("late middleware calls = %d, want 1", calls)
	}
}

func TestBearNoRoutePreservesPluginDispatchAndCustomFallback(t *testing.T) {
	resetGinModeForTest(t)
	config := NewSysConfig()
	config.SetFrameworkStrict(true)
	app := Ignite(config)
	app.pluginMode = true
	err := app.HandleE(http.MethodGet, "/plugin-with-fallback", func() string { return "plugin" })
	app.pluginMode = false
	if err != nil {
		t.Fatalf("HandleE() error = %v", err)
	}
	if err := app.ApplyAll(context.Background()); err != nil {
		t.Fatalf("ApplyAll() error = %v", err)
	}
	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })
	app.NoRoute(func(ctx *gin.Context) {
		ctx.String(http.StatusNotFound, "custom fallback")
	})

	pluginResponse := dispatchPluginRequest(app, http.MethodGet, "/plugin-with-fallback")
	if pluginResponse.Code != http.StatusOK || pluginResponse.Body.String() != `"plugin"` {
		t.Fatalf("plugin response = %d %q", pluginResponse.Code, pluginResponse.Body.String())
	}
	fallbackResponse := dispatchPluginRequest(app, http.MethodGet, "/missing")
	if fallbackResponse.Code != http.StatusNotFound || fallbackResponse.Body.String() != "custom fallback" {
		t.Fatalf("fallback response = %d %q", fallbackResponse.Code, fallbackResponse.Body.String())
	}
}

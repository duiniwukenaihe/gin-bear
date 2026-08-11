package bear

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestCasbinEnforcerIsolationBetweenBearInstances(t *testing.T) {
	resetTestInjector()
	t.Cleanup(resetTestInjector)
	resetGinModeForTest(t)

	allow := newMemoryCasbinEnforcer(t)
	if added, err := allow.AddPolicy("anonymous", "/private", http.MethodGet); err != nil || !added {
		t.Fatalf("add allow policy: added=%v err=%v", added, err)
	}
	deny := newMemoryCasbinEnforcer(t)

	allowApp, _ := newCasbinIsolationApp(t, allow)
	denyApp, _ := newCasbinIsolationApp(t, deny)

	assertCasbinStatus(t, allowApp, http.StatusOK)
	assertCasbinStatus(t, denyApp, http.StatusForbidden)
}

func TestCasbinEnforcerIsolationDoesNotUseAnotherBearGlobal(t *testing.T) {
	resetTestInjector()
	t.Cleanup(resetTestInjector)
	resetGinModeForTest(t)

	isolatedApp, isolatedFairing := newCasbinIsolationApp(t, nil)
	otherEnforcer := newMemoryCasbinEnforcer(t)
	if added, err := otherEnforcer.AddPolicy("anonymous", "/private", http.MethodGet); err != nil || !added {
		t.Fatalf("add other Bear policy: added=%v err=%v", added, err)
	}
	otherApp, _ := newCasbinIsolationApp(t, otherEnforcer)
	assertCasbinStatus(t, otherApp, http.StatusOK)

	response := serveCasbinRequest(isolatedApp)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("isolated Bear status = %d, want 500; body = %s", response.Code, response.Body.String())
	}
	if isolatedFairing.Enforcer != nil {
		t.Fatal("CasbinFairing captured an enforcer from another Bear")
	}
	if strings.Contains(response.Body.String(), "CasbinEnforcer") {
		t.Fatalf("response leaked Casbin dependency details: %s", response.Body.String())
	}
}

func TestCasbinEnforceErrorReturnsGeneric500(t *testing.T) {
	const internalDetail = "sensitive authorization backend detail"
	fairing := &CasbinFairing{Enforcer: newFailingCasbinEnforcer(t, internalDetail)}
	ctx := newCasbinRequestContext()

	err := fairing.OnRequest(ctx)
	if err == nil {
		t.Fatal("OnRequest returned no error")
	}
	if strings.Contains(err.Error(), internalDetail) {
		t.Fatalf("OnRequest error leaked internal detail: %v", err)
	}
	status, response := errorResponse(ctx, err)
	if status != http.StatusInternalServerError || response.Message != "Internal server error" {
		t.Fatalf("public error = status:%d response:%#v", status, response)
	}
}

func TestCasbinEnforceErrorIsLogged(t *testing.T) {
	const internalDetail = "sensitive authorization backend detail"
	previousLogger := slog.Default()
	var logs bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	fairing := &CasbinFairing{Enforcer: newFailingCasbinEnforcer(t, internalDetail)}
	if err := fairing.OnRequest(newCasbinRequestContext()); err == nil {
		t.Fatal("OnRequest returned no error")
	}
	if !strings.Contains(logs.String(), internalDetail) {
		t.Fatalf("enforcement error was not logged: %s", logs.String())
	}
}

func newCasbinIsolationApp(t *testing.T, enforcer *CasbinEnforcer) (*Bear, *CasbinFairing) {
	t.Helper()
	cfg := NewSysConfig()
	cfg.DB.Enabled = false
	app := Ignite(cfg)
	if enforcer != nil {
		app.Beans(enforcer)
	}
	fairing := NewCasbinFairing()
	app.Attach(fairing)
	app.Handle(http.MethodGet, "/private", func() string { return "ok" })
	if err := app.ApplyAll(context.Background()); err != nil {
		t.Fatalf("ApplyAll failed: %v", err)
	}
	return app, fairing
}

func newMemoryCasbinEnforcer(t *testing.T) *CasbinEnforcer {
	t.Helper()
	enforcer, err := NewCasbinEnforcer(nil, nil)
	if err != nil {
		t.Fatalf("NewCasbinEnforcer failed: %v", err)
	}
	return enforcer
}

func newFailingCasbinEnforcer(t *testing.T, detail string) *CasbinEnforcer {
	t.Helper()
	const modelText = `
[request_definition]
r = sub, obj, act

[policy_definition]
p = sub, obj, act

[policy_effect]
e = some(where (p.eft == allow))

[matchers]
m = failAuthorization(r.sub)
`
	enforcer, err := NewCasbinEnforcer(nil, &CasbinConfig{ModelText: modelText})
	if err != nil {
		t.Fatalf("NewCasbinEnforcer failed: %v", err)
	}
	enforcer.AddFunction("failAuthorization", func(...interface{}) (interface{}, error) {
		return false, errors.New(detail)
	})
	if added, err := enforcer.AddPolicy("1", "/private", http.MethodGet); err != nil || !added {
		t.Fatalf("add failing policy: added=%v err=%v", added, err)
	}
	return enforcer
}

func newCasbinRequestContext() *gin.Context {
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodGet, "/private", nil)
	ctx.Set("current_user_id", uint(1))
	return ctx
}

func assertCasbinStatus(t *testing.T, app *Bear, want int) {
	t.Helper()
	response := serveCasbinRequest(app)
	if response.Code != want {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, want, response.Body.String())
	}
}

func serveCasbinRequest(app *Bear) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/private", nil))
	return response
}

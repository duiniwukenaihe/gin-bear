//go:build !bear_no_casbin && !bear_casbin_no_gorm_adapter

package bear

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// TestCasbinAuthorizerGrantAndRevoke proves the controlled interface grants
// and revokes without any explicit cache management.
func TestCasbinAuthorizerGrantAndRevoke(t *testing.T) {
	authorizer, err := NewCasbinAuthorizer(nil, nil)
	if err != nil {
		t.Fatalf("NewCasbinAuthorizer failed: %v", err)
	}
	ctx := context.Background()
	allowed, err := authorizer.Authorize(ctx, AuthorizationRequest{Subject: "alice", Resource: "/secret", Action: "GET"})
	if err != nil || allowed {
		t.Fatalf("Authorize before grant = %v, %v; want false, nil", allowed, err)
	}
	if added, err := authorizer.AddPolicy("admin", "/secret", "GET"); err != nil || !added {
		t.Fatalf("AddPolicy = %v, %v; want true, nil", added, err)
	}
	if added, err := authorizer.AddGroupingPolicy("alice", "admin"); err != nil || !added {
		t.Fatalf("AddGroupingPolicy = %v, %v; want true, nil", added, err)
	}
	if allowed, err := authorizer.Authorize(ctx, AuthorizationRequest{Subject: "alice", Resource: "/secret", Action: "GET"}); err != nil || !allowed {
		t.Fatalf("Authorize after grant = %v, %v; want true, nil", allowed, err)
	}
	if removed, err := authorizer.RemoveGroupingPolicy("alice", "admin"); err != nil || !removed {
		t.Fatalf("RemoveGroupingPolicy = %v, %v; want true, nil", removed, err)
	}
	// Revocation is visible on the very next authorization: no cache to clear.
	if allowed, err := authorizer.Authorize(ctx, AuthorizationRequest{Subject: "alice", Resource: "/secret", Action: "GET"}); err != nil {
		t.Fatalf("Authorize after revoke failed: %v", err)
	} else if allowed {
		t.Fatal("Authorize after revoke = true, want false")
	}
}

// TestCasbinAuthorizerNoChangeIsNotFailure ensures duplicate adds and removals
// of absent rules report false, nil and keep the authorizer available.
func TestCasbinAuthorizerNoChangeIsNotFailure(t *testing.T) {
	authorizer, err := NewCasbinAuthorizer(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if added, err := authorizer.AddPolicy("admin", "/secret", "GET"); err != nil || !added {
		t.Fatalf("AddPolicy = %v, %v", added, err)
	}
	if added, err := authorizer.AddPolicy("admin", "/secret", "GET"); err != nil || added {
		t.Fatalf("duplicate AddPolicy = %v, %v; want false, nil", added, err)
	}
	if removed, err := authorizer.RemovePolicy("nobody", "/nothing", "GET"); err != nil || removed {
		t.Fatalf("absent RemovePolicy = %v, %v; want false, nil", removed, err)
	}
	if removed, err := authorizer.RemoveGroupingPolicy("ghost", "admin"); err != nil || removed {
		t.Fatalf("absent RemoveGroupingPolicy = %v, %v; want false, nil", removed, err)
	}
	// Still available: no-change results must not trip the fail-closed latch.
	if _, err := authorizer.Authorize(context.Background(), AuthorizationRequest{Subject: "admin", Resource: "/secret", Action: "GET"}); err != nil {
		t.Fatalf("Authorize after no-change = %v, want nil", err)
	}
}

// TestCasbinAuthorizerConcurrentReadWrite runs real authorization reads
// against a concurrent revocation under -race. Ordering is established with a
// channel: only authorizations started after the revocation returns must
// observe the new policy; in-flight reads are never required to deny.
func TestCasbinAuthorizerConcurrentReadWrite(t *testing.T) {
	authorizer, err := NewCasbinAuthorizer(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authorizer.AddPolicy("admin", "/secret", "GET"); err != nil {
		t.Fatal(err)
	}
	if _, err := authorizer.AddGroupingPolicy("alice", "admin"); err != nil {
		t.Fatal(err)
	}
	request := AuthorizationRequest{Subject: "alice", Resource: "/secret", Action: "GET"}
	revoked := make(chan struct{})
	var readers sync.WaitGroup
	for i := 0; i < 8; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for j := 0; j < 200; j++ {
				select {
				case <-revoked:
					return
				default:
				}
				// In-flight reads may allow or deny; they must never error.
				if _, err := authorizer.Authorize(context.Background(), request); err != nil {
					t.Errorf("concurrent Authorize failed: %v", err)
					return
				}
			}
		}()
	}
	if removed, err := authorizer.RemoveGroupingPolicy("alice", "admin"); err != nil || !removed {
		t.Fatalf("RemoveGroupingPolicy = %v, %v", removed, err)
	}
	close(revoked)
	readers.Wait()
	// Every authorization started after the revocation returned must deny.
	for i := 0; i < 50; i++ {
		if allowed, err := authorizer.Authorize(context.Background(), request); err != nil {
			t.Fatalf("post-revocation Authorize failed: %v", err)
		} else if allowed {
			t.Fatal("post-revocation Authorize = true, want false")
		}
	}
}

// TestCasbinAuthorizerInstancesAreIsolated ensures each authorizer owns
// independent in-memory policy.
func TestCasbinAuthorizerInstancesAreIsolated(t *testing.T) {
	first, err := NewCasbinAuthorizer(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewCasbinAuthorizer(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.AddPolicy("admin", "/secret", "GET"); err != nil {
		t.Fatal(err)
	}
	if _, err := first.AddGroupingPolicy("alice", "admin"); err != nil {
		t.Fatal(err)
	}
	request := AuthorizationRequest{Subject: "alice", Resource: "/secret", Action: "GET"}
	if allowed, err := first.Authorize(context.Background(), request); err != nil || !allowed {
		t.Fatalf("first Authorize = %v, %v; want true, nil", allowed, err)
	}
	if allowed, err := second.Authorize(context.Background(), request); err != nil || allowed {
		t.Fatalf("second Authorize = %v, %v; want false, nil", allowed, err)
	}
}

// TestCasbinAuthorizerRejectsScope refuses to silently drop tenant/project scope.
func TestCasbinAuthorizerRejectsScope(t *testing.T) {
	authorizer, err := NewCasbinAuthorizer(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = authorizer.Authorize(context.Background(), AuthorizationRequest{
		Subject: "alice", Resource: "/secret", Action: "GET",
		Scope: map[string]string{"project": "p-1"},
	})
	if err == nil || !strings.Contains(err.Error(), "scope") {
		t.Fatalf("scoped Authorize error = %v, want scope rejection", err)
	}
}

// TestCasbinAuthorizerHonorsCancellation surfaces caller cancellation instead
// of authorizing against a dead request.
func TestCasbinAuthorizerHonorsCancellation(t *testing.T) {
	authorizer, err := NewCasbinAuthorizer(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authorizer.AddPolicy("admin", "/secret", "GET"); err != nil {
		t.Fatal(err)
	}
	if _, err := authorizer.AddGroupingPolicy("alice", "admin"); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := authorizer.Authorize(cancelled, AuthorizationRequest{Subject: "alice", Resource: "/secret", Action: "GET"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Authorize error = %v, want context.Canceled", err)
	}
	if allowed, err := authorizer.Authorize(context.Background(), AuthorizationRequest{Subject: "alice", Resource: "/secret", Action: "GET"}); err != nil || !allowed {
		t.Fatalf("live Authorize = %v, %v; want true, nil", allowed, err)
	}
}

// TestCasbinAuthorizerRejectsNonTripleModel keeps ABAC/domain models on their
// own custom Authorizer instead of silently mis-evaluating them.
func TestCasbinAuthorizerRejectsNonTripleModel(t *testing.T) {
	const domainModel = `
[request_definition]
r = sub, dom, obj, act

[policy_definition]
p = sub, dom, obj, act

[role_definition]
g = _, _, _

[policy_effect]
e = some(where (p.eft == allow))

[matchers]
m = g(r.sub, p.sub, r.dom) && r.obj == p.obj && r.act == p.act
`
	if _, err := NewCasbinAuthorizer(nil, &CasbinConfig{ModelText: domainModel}); err == nil {
		t.Fatal("domain model was accepted, want construction rejection")
	}
}

// TestCasbinAuthorizerRejectsNonStringParams fails closed on caller type errors
// instead of panicking in the underlying type assertion.
func TestCasbinAuthorizerRejectsNonStringParams(t *testing.T) {
	authorizer, err := NewCasbinAuthorizer(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authorizer.AddPolicy("admin", "/secret", 42); err == nil {
		t.Fatal("non-string policy parameter was accepted")
	}
}

// sharedAuthorizerDB opens one SQLite file shared by two authorizer instances
// behind independent adapters, mirroring two processes on one policy database.
func sharedAuthorizerDB(t *testing.T) (*gorm.DB, *GormAdapter, *GormAdapter) {
	t.Helper()
	path := t.TempDir() + "/casbin-shared.db"
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db, &GormAdapter{DB: db}, &GormAdapter{DB: db}
}

// TestCasbinAuthorizerMultiInstanceReload requires a persisted revocation to
// take effect on the other instance's next authorization without an external
// notification or an operator-triggered reload.
func TestCasbinAuthorizerMultiInstanceReload(t *testing.T) {
	_, firstAdapter, secondAdapter := sharedAuthorizerDB(t)
	first, err := NewCasbinAuthorizer(firstAdapter, nil)
	if err != nil {
		t.Fatalf("first NewCasbinAuthorizer failed: %v", err)
	}
	second, err := NewCasbinAuthorizer(secondAdapter, nil)
	if err != nil {
		t.Fatalf("second NewCasbinAuthorizer failed: %v", err)
	}
	if _, err := first.AddPolicy("admin", "/secret", "GET"); err != nil {
		t.Fatalf("first AddPolicy failed: %v", err)
	}
	if _, err := first.AddGroupingPolicy("alice", "admin"); err != nil {
		t.Fatalf("first AddGroupingPolicy failed: %v", err)
	}
	request := AuthorizationRequest{Subject: "alice", Resource: "/secret", Action: "GET"}
	if err := second.LoadPolicy(); err != nil {
		t.Fatalf("second LoadPolicy failed: %v", err)
	}
	if allowed, err := second.Authorize(context.Background(), request); err != nil || !allowed {
		t.Fatalf("second Authorize after load = %v, %v; want true, nil", allowed, err)
	}
	if _, err := first.RemoveGroupingPolicy("alice", "admin"); err != nil {
		t.Fatalf("first RemoveGroupingPolicy failed: %v", err)
	}
	if allowed, err := second.Authorize(context.Background(), request); err != nil {
		t.Fatalf("second Authorize after remote revocation failed: %v", err)
	} else if allowed {
		t.Fatal("second Authorize after remote revocation = true, want false")
	}
}

func TestCasbinAuthorizerRemoteRevocationWithoutPriorRead(t *testing.T) {
	_, firstAdapter, secondAdapter := sharedAuthorizerDB(t)
	first, err := NewCasbinAuthorizer(firstAdapter, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewCasbinAuthorizer(secondAdapter, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.AddPolicy("admin", "/secret", "GET"); err != nil {
		t.Fatal(err)
	}
	if _, err := first.AddGroupingPolicy("alice", "admin"); err != nil {
		t.Fatal(err)
	}
	// The second instance has never authorized or reloaded since the grant.
	// Revocation still has to remove the persisted rule.
	if removed, err := second.RemoveGroupingPolicy("alice", "admin"); err != nil || !removed {
		t.Fatalf("remote RemoveGroupingPolicy = %v, %v; want true, nil", removed, err)
	}
	request := AuthorizationRequest{Subject: "alice", Resource: "/secret", Action: "GET"}
	if allowed, err := first.Authorize(context.Background(), request); err != nil || allowed {
		t.Fatalf("first Authorize after remote revoke = %v, %v; want false, nil", allowed, err)
	}
}

// TestCasbinAuthorizerFailureClosesAndReloadRecovers proves fail-closed reads
// after a backend failure and recovery through a successful reload.
func TestCasbinAuthorizerFailureClosesAndReloadRecovers(t *testing.T) {
	db, firstAdapter, _ := sharedAuthorizerDB(t)
	authorizer, err := NewCasbinAuthorizer(firstAdapter, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authorizer.AddPolicy("admin", "/secret", "GET"); err != nil {
		t.Fatal(err)
	}
	if _, err := authorizer.AddGroupingPolicy("alice", "admin"); err != nil {
		t.Fatal(err)
	}
	request := AuthorizationRequest{Subject: "alice", Resource: "/secret", Action: "GET"}
	if allowed, err := authorizer.Authorize(context.Background(), request); err != nil || !allowed {
		t.Fatalf("Authorize before failure = %v, %v; want true, nil", allowed, err)
	}

	// Break the backend: dropping the rule table makes the next persistence
	// and the next load fail.
	if err := db.Exec("DROP TABLE casbin_rule").Error; err != nil {
		t.Fatalf("drop casbin_rule failed (table name changed?): %v", err)
	}
	if _, err := authorizer.AddPolicy("viewer", "/public", "GET"); err == nil {
		t.Fatal("write against a broken backend succeeded, want failure")
	}
	// Fail-closed: no stale allow after the backend failure.
	if _, err := authorizer.Authorize(context.Background(), request); err == nil {
		t.Fatal("Authorize after backend failure succeeded, want fail-closed error")
	}

	// A fresh instance recreates the table through adapter auto-migration and
	// persists the current policy; the failed instance recovers on reload.
	rebuilt, err := NewCasbinAuthorizer(&GormAdapter{DB: db}, nil)
	if err != nil {
		t.Fatalf("rebuild NewCasbinAuthorizer failed: %v", err)
	}
	if _, err := rebuilt.AddPolicy("admin", "/secret", "GET"); err != nil {
		t.Fatalf("rebuild AddPolicy failed: %v", err)
	}
	if _, err := rebuilt.AddGroupingPolicy("alice", "admin"); err != nil {
		t.Fatalf("rebuild AddGroupingPolicy failed: %v", err)
	}
	if err := authorizer.LoadPolicy(); err != nil {
		t.Fatalf("reload after repair failed: %v", err)
	}
	if allowed, err := authorizer.Authorize(context.Background(), request); err != nil || !allowed {
		t.Fatalf("Authorize after reload = %v, %v; want true, nil", allowed, err)
	}
}

type identityFairing struct {
	BaseFairing
	subject string
}

func (f *identityFairing) OnRequest(ctx *gin.Context) error {
	ctx.Set("current_user_id", f.subject)
	return nil
}

func (f *identityFairing) Name() string { return "testIdentity" }

func serveAuthorizerRequest(t *testing.T, authorizer *CasbinAuthorizer) *httptest.ResponseRecorder {
	t.Helper()
	resetTestInjector()
	t.Cleanup(resetTestInjector)
	resetGinModeForTest(t)
	cfg := NewSysConfig()
	cfg.DB.Enabled = false
	cfg.SetFrameworkStrict(true)
	app := Ignite(cfg)
	if err := app.Runtime().Container.TrySetWithInterface((*Authorizer)(nil), authorizer); err != nil {
		t.Fatalf("TrySetWithInterface failed: %v", err)
	}
	app.Attach(&identityFairing{subject: "alice"})
	permission := NewPermissionFairing("/secret", "GET", nil)
	permission.Authorizer = authorizer
	app.Attach(permission)
	app.Handle(http.MethodGet, "/secret", func() string { return "ok" })
	if err := app.ApplyAll(context.Background()); err != nil {
		t.Fatalf("ApplyAll failed: %v", err)
	}
	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/secret", nil))
	return response
}

// TestCasbinAuthorizerHTTPAllowAndDeny exercises the documented wiring:
// TrySetWithInterface registration plus route-level PermissionFairing with
// explicit resource and action.
func TestCasbinAuthorizerHTTPAllowAndDeny(t *testing.T) {
	authorizer, err := NewCasbinAuthorizer(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authorizer.AddPolicy("admin", "/secret", "GET"); err != nil {
		t.Fatal(err)
	}
	if _, err := authorizer.AddGroupingPolicy("alice", "admin"); err != nil {
		t.Fatal(err)
	}
	if response := serveAuthorizerRequest(t, authorizer); response.Code != http.StatusOK {
		t.Fatalf("allowed status = %d, want 200; body = %s", response.Code, response.Body.String())
	}
	if _, err := authorizer.RemoveGroupingPolicy("alice", "admin"); err != nil {
		t.Fatal(err)
	}
	if response := serveAuthorizerRequest(t, authorizer); response.Code != http.StatusForbidden {
		t.Fatalf("revoked status = %d, want 403; body = %s", response.Code, response.Body.String())
	}
}

// TestCasbinAuthorizerHTTPFailureIsGeneric500 ensures a failed backend maps to
// a generic 500 without leaking policy storage details.
func TestCasbinAuthorizerHTTPFailureIsGeneric500(t *testing.T) {
	db, adapter, _ := sharedAuthorizerDB(t)
	authorizer, err := NewCasbinAuthorizer(adapter, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authorizer.AddPolicy("admin", "/secret", "GET"); err != nil {
		t.Fatal(err)
	}
	if _, err := authorizer.AddGroupingPolicy("alice", "admin"); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("DROP TABLE casbin_rule").Error; err != nil {
		t.Fatalf("drop casbin_rule failed: %v", err)
	}
	if _, err := authorizer.AddPolicy("viewer", "/public", "GET"); err == nil {
		t.Fatal("write against a broken backend succeeded")
	}
	response := serveAuthorizerRequest(t, authorizer)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("failed-backend status = %d, want 500; body = %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "casbin") {
		t.Fatalf("failed-backend response leaked internals: %s", response.Body.String())
	}
}

// TestCasbinAuthorizerRejectsMalformedPolicyRules is a P1 regression: a rule
// with the wrong arity must be rejected before touching memory or the
// database. Previously AddPolicy("broken") returned true, nil, persisted a bad
// row, and broke subsequent LoadPolicy and new instances.
func TestCasbinAuthorizerRejectsMalformedPolicyRules(t *testing.T) {
	_, firstAdapter, _ := sharedAuthorizerDB(t)
	authorizer, err := NewCasbinAuthorizer(firstAdapter, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authorizer.AddPolicy("admin", "/secret", "GET"); err != nil {
		t.Fatal(err)
	}
	if _, err := authorizer.AddGroupingPolicy("alice", "admin"); err != nil {
		t.Fatal(err)
	}
	request := AuthorizationRequest{Subject: "alice", Resource: "/secret", Action: "GET"}

	malformed := []struct {
		name string
		call func() (bool, error)
	}{
		{"policy too short", func() (bool, error) { return authorizer.AddPolicy("broken") }},
		{"policy too long", func() (bool, error) { return authorizer.AddPolicy("a", "b", "c", "d") }},
		{"policy empty", func() (bool, error) { return authorizer.AddPolicy() }},
		{"grouping too short", func() (bool, error) { return authorizer.AddGroupingPolicy("lonely") }},
		{"grouping too long", func() (bool, error) { return authorizer.AddGroupingPolicy("a", "b", "c") }},
		{"remove policy wrong arity", func() (bool, error) { return authorizer.RemovePolicy("broken") }},
		{"remove grouping wrong arity", func() (bool, error) { return authorizer.RemoveGroupingPolicy("a", "b", "c") }},
	}
	for _, tt := range malformed {
		t.Run(tt.name, func(t *testing.T) {
			if changed, err := tt.call(); err == nil || changed {
				t.Fatalf("malformed write = (%v, %v), want (false, arity error)", changed, err)
			}
		})
	}

	// Rejection leaves the instance fully usable: authorize, reload, restart.
	if allowed, err := authorizer.Authorize(context.Background(), request); err != nil || !allowed {
		t.Fatalf("Authorize after rejections = %v, %v; want true, nil", allowed, err)
	}
	if err := authorizer.LoadPolicy(); err != nil {
		t.Fatalf("LoadPolicy after rejections failed: %v", err)
	}
	if allowed, err := authorizer.Authorize(context.Background(), request); err != nil || !allowed {
		t.Fatalf("Authorize after reload = %v, %v; want true, nil", allowed, err)
	}
	restarted, err := NewCasbinAuthorizer(firstAdapter, nil)
	if err != nil {
		t.Fatalf("restart after rejections failed: %v", err)
	}
	if allowed, err := restarted.Authorize(context.Background(), request); err != nil || !allowed {
		t.Fatalf("restarted Authorize = %v, %v; want true, nil", allowed, err)
	}
}

// TestCasbinAuthorizerMemoryLoadPolicyPreservesPolicy is a P2 regression:
// reloading a memory-mode authorizer must not panic. It returns an
// identifiable error and keeps the existing policy usable.
func TestCasbinAuthorizerMemoryLoadPolicyPreservesPolicy(t *testing.T) {
	authorizer, err := NewCasbinAuthorizer(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authorizer.AddPolicy("admin", "/secret", "GET"); err != nil {
		t.Fatal(err)
	}
	if _, err := authorizer.AddGroupingPolicy("alice", "admin"); err != nil {
		t.Fatal(err)
	}
	request := AuthorizationRequest{Subject: "alice", Resource: "/secret", Action: "GET"}
	if err := authorizer.LoadPolicy(); !errors.Is(err, ErrCasbinReloadRequiresAdapter) {
		t.Fatalf("memory LoadPolicy error = %v, want ErrCasbinReloadRequiresAdapter", err)
	}
	if allowed, err := authorizer.Authorize(context.Background(), request); err != nil || !allowed {
		t.Fatalf("Authorize after memory reload = %v, %v; want true, nil", allowed, err)
	}
}

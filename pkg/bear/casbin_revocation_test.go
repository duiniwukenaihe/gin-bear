//go:build !bear_no_casbin && !bear_casbin_no_gorm_adapter

package bear

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestCasbinRevocationTakesEffectWithoutExplicitCacheClear covers the P1
// production defect: removing a role kept returning the cached allow until the
// caller cleared the cache manually.
func TestCasbinRevocationTakesEffectWithoutExplicitCacheClear(t *testing.T) {
	resetTestInjector()
	t.Cleanup(resetTestInjector)
	resetGinModeForTest(t)

	enforcer, err := NewCasbinEnforcer(nil, nil)
	if err != nil {
		t.Fatalf("NewCasbinEnforcer failed: %v", err)
	}
	if _, err := enforcer.AddPolicy("admin", "/secret", http.MethodGet); err != nil {
		t.Fatalf("AddPolicy failed: %v", err)
	}
	if _, err := enforcer.AddGroupingPolicy("alice", "admin"); err != nil {
		t.Fatalf("AddGroupingPolicy failed: %v", err)
	}

	allowed, err := enforcer.Enforce("alice", "/secret", http.MethodGet)
	if err != nil || !allowed {
		t.Fatalf("Enforce before revocation = %v, %v; want true, nil", allowed, err)
	}
	if _, err := enforcer.RemoveGroupingPolicy("alice", "admin"); err != nil {
		t.Fatalf("RemoveGroupingPolicy failed: %v", err)
	}
	// No explicit InvalidateCache/ClearCache here: revocation must be visible
	// on the very next enforcement.
	allowed, err = enforcer.Enforce("alice", "/secret", http.MethodGet)
	if err != nil {
		t.Fatalf("Enforce after revocation failed: %v", err)
	}
	if allowed {
		t.Fatal("Enforce after RemoveGroupingPolicy = true, want false without explicit cache clear")
	}
}

// TestCasbinRevocationNegativeCacheGrantsAfterAssignment ensures disabling the
// decision cache does not freeze denials either.
func TestCasbinRevocationNegativeCacheGrantsAfterAssignment(t *testing.T) {
	enforcer, err := NewCasbinEnforcer(nil, nil)
	if err != nil {
		t.Fatalf("NewCasbinEnforcer failed: %v", err)
	}
	allowed, err := enforcer.Enforce("bob", "/secret", http.MethodGet)
	if err != nil || allowed {
		t.Fatalf("Enforce before grant = %v, %v; want false, nil", allowed, err)
	}
	if _, err := enforcer.AddPolicy("admin", "/secret", http.MethodGet); err != nil {
		t.Fatalf("AddPolicy failed: %v", err)
	}
	if _, err := enforcer.AddGroupingPolicy("bob", "admin"); err != nil {
		t.Fatalf("AddGroupingPolicy failed: %v", err)
	}
	allowed, err = enforcer.Enforce("bob", "/secret", http.MethodGet)
	if err != nil || !allowed {
		t.Fatalf("Enforce after grant = %v, %v; want true, nil", allowed, err)
	}
}

// TestCasbinRevocationPolicyRemovalDeniesRoleMembers covers the case where the
// request key (alice) differs from the policy key (admin): removing the role's
// policy must deny the member even if the member's own decision was cached.
func TestCasbinRevocationPolicyRemovalDeniesRoleMembers(t *testing.T) {
	enforcer, err := NewCasbinEnforcer(nil, nil)
	if err != nil {
		t.Fatalf("NewCasbinEnforcer failed: %v", err)
	}
	if _, err := enforcer.AddPolicy("admin", "/secret", http.MethodGet); err != nil {
		t.Fatalf("AddPolicy failed: %v", err)
	}
	if _, err := enforcer.AddGroupingPolicy("alice", "admin"); err != nil {
		t.Fatalf("AddGroupingPolicy failed: %v", err)
	}
	if allowed, err := enforcer.Enforce("alice", "/secret", http.MethodGet); err != nil || !allowed {
		t.Fatalf("Enforce before policy removal = %v, %v; want true, nil", allowed, err)
	}
	if _, err := enforcer.RemovePolicy("admin", "/secret", http.MethodGet); err != nil {
		t.Fatalf("RemovePolicy failed: %v", err)
	}
	if allowed, err := enforcer.Enforce("alice", "/secret", http.MethodGet); err != nil {
		t.Fatalf("Enforce after policy removal failed: %v", err)
	} else if allowed {
		t.Fatal("Enforce after RemovePolicy = true, want false")
	}
}

// TestCasbinRevocationNamedAndFilteredRemoval ensures the fix is not scoped to
// a single removal method.
func TestCasbinRevocationNamedAndFilteredRemoval(t *testing.T) {
	enforcer, err := NewCasbinEnforcer(nil, nil)
	if err != nil {
		t.Fatalf("NewCasbinEnforcer failed: %v", err)
	}
	if _, err := enforcer.AddPolicy("admin", "/secret", http.MethodGet); err != nil {
		t.Fatalf("AddPolicy failed: %v", err)
	}
	if _, err := enforcer.AddGroupingPolicy("alice", "admin"); err != nil {
		t.Fatalf("AddGroupingPolicy failed: %v", err)
	}
	if allowed, err := enforcer.Enforce("alice", "/secret", http.MethodGet); err != nil || !allowed {
		t.Fatalf("Enforce before removal = %v, %v; want true, nil", allowed, err)
	}
	if _, err := enforcer.RemoveNamedGroupingPolicy("g", "alice", "admin"); err != nil {
		t.Fatalf("RemoveNamedGroupingPolicy failed: %v", err)
	}
	if allowed, err := enforcer.Enforce("alice", "/secret", http.MethodGet); err != nil {
		t.Fatalf("Enforce after named grouping removal failed: %v", err)
	} else if allowed {
		t.Fatal("Enforce after RemoveNamedGroupingPolicy = true, want false")
	}

	// Re-grant then remove via filtered policy removal.
	if _, err := enforcer.AddGroupingPolicy("alice", "admin"); err != nil {
		t.Fatalf("re-add grouping failed: %v", err)
	}
	if allowed, err := enforcer.Enforce("alice", "/secret", http.MethodGet); err != nil || !allowed {
		t.Fatalf("Enforce after re-grant = %v, %v; want true, nil", allowed, err)
	}
	if _, err := enforcer.RemoveFilteredPolicy(0, "admin"); err != nil {
		t.Fatalf("RemoveFilteredPolicy failed: %v", err)
	}
	if allowed, err := enforcer.Enforce("alice", "/secret", http.MethodGet); err != nil {
		t.Fatalf("Enforce after filtered removal failed: %v", err)
	} else if allowed {
		t.Fatal("Enforce after RemoveFilteredPolicy = true, want false")
	}

	// ClearPolicy must also be immediately visible.
	if _, err := enforcer.AddPolicy("admin", "/secret", http.MethodGet); err != nil {
		t.Fatalf("re-add policy failed: %v", err)
	}
	if allowed, err := enforcer.Enforce("alice", "/secret", http.MethodGet); err != nil || !allowed {
		t.Fatalf("Enforce before ClearPolicy = %v, %v; want true, nil", allowed, err)
	}
	enforcer.ClearPolicy()
	if allowed, err := enforcer.Enforce("alice", "/secret", http.MethodGet); err != nil {
		t.Fatalf("Enforce after ClearPolicy failed: %v", err)
	} else if allowed {
		t.Fatal("Enforce after ClearPolicy = true, want false")
	}
}

// TestCasbinRevocationThroughHTTP verifies 200 -> 403 through the real Bear +
// CasbinFairing path while keeping the container isolation and error-redaction
// guarantees covered by the existing isolation tests.
func TestCasbinRevocationThroughHTTP(t *testing.T) {
	resetTestInjector()
	t.Cleanup(resetTestInjector)
	resetGinModeForTest(t)

	enforcer, err := NewCasbinEnforcer(nil, nil)
	if err != nil {
		t.Fatalf("NewCasbinEnforcer failed: %v", err)
	}
	if _, err := enforcer.AddPolicy("admin", "/private", http.MethodGet); err != nil {
		t.Fatalf("AddPolicy failed: %v", err)
	}
	if _, err := enforcer.AddGroupingPolicy("anonymous", "admin"); err != nil {
		t.Fatalf("AddGroupingPolicy failed: %v", err)
	}

	app, _ := newCasbinIsolationApp(t, enforcer)
	assertCasbinStatus(t, app, http.StatusOK)

	if _, err := enforcer.RemoveGroupingPolicy("anonymous", "admin"); err != nil {
		t.Fatalf("RemoveGroupingPolicy failed: %v", err)
	}
	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/private", nil))
	if response.Code != http.StatusForbidden {
		t.Fatalf("status after revocation = %d, want %d; body = %s", response.Code, http.StatusForbidden, response.Body.String())
	}
}

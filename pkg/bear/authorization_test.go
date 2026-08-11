package bear

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

type recordingAuthorizer struct {
	request AuthorizationRequest
	allowed bool
	err     error
}

func (a *recordingAuthorizer) Authorize(_ context.Context, request AuthorizationRequest) (bool, error) {
	a.request = request
	if request.Scope != nil {
		request.Scope["mutated"] = "inside-authorizer"
	}
	return a.allowed, a.err
}

func TestPermissionFairingAuthorizesResourceActionAndCopiedScope(t *testing.T) {
	ctx := newPermissionContext(t)
	ctx.Set("current_user_id", uint(42))
	originalScope := map[string]string{"project": "p-1"}
	authorizer := &recordingAuthorizer{allowed: true}
	fairing := NewPermissionFairing("deployment", "restart", func(*gin.Context) (map[string]string, error) {
		return originalScope, nil
	})
	fairing.Authorizer = authorizer

	if err := fairing.OnRequest(ctx); err != nil {
		t.Fatalf("OnRequest() error = %v", err)
	}
	if authorizer.request.Subject != "42" || authorizer.request.Resource != "deployment" || authorizer.request.Action != "restart" {
		t.Fatalf("authorization request = %#v", authorizer.request)
	}
	if authorizer.request.Scope["project"] != "p-1" {
		t.Fatalf("authorization scope = %#v", authorizer.request.Scope)
	}
	if _, changed := originalScope["mutated"]; changed {
		t.Fatalf("authorizer mutated resolver-owned scope: %#v", originalScope)
	}
}

func TestPermissionFairingRejectsMissingSubject(t *testing.T) {
	fairing := NewPermissionFairing("server", "connect", nil)
	fairing.Authorizer = &recordingAuthorizer{allowed: true}

	if err := fairing.OnRequest(newPermissionContext(t)); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("OnRequest() error = %v, want ErrUnauthorized", err)
	}
}

func TestPermissionFairingDefaultSubjectRequiresStrictPipeline(t *testing.T) {
	ctx := newPermissionContext(t)
	ctx.Set("current_user_id", "user-compat")
	ctx.Set(runtimeContextKey, &Runtime{Config: NewSysConfig()})
	fairing := NewPermissionFairing("server", "connect", nil)
	fairing.Authorizer = &recordingAuthorizer{allowed: true}

	if err := fairing.OnRequest(ctx); !errors.Is(err, ErrInternalServer) {
		t.Fatalf("compatibility OnRequest() error = %v, want ErrInternalServer", err)
	}

	fairing.SetSubjectResolver(func(*gin.Context) (string, error) { return "custom-user", nil })
	if err := fairing.OnRequest(ctx); err != nil {
		t.Fatalf("custom resolver OnRequest() error = %v", err)
	}

	strictConfig := NewSysConfig()
	strictConfig.SetFrameworkStrict(true)
	ctx.Set(runtimeContextKey, &Runtime{Config: strictConfig})
	fairing.SetSubjectResolver(nil)
	if err := fairing.OnRequest(ctx); err != nil {
		t.Fatalf("strict default resolver OnRequest() error = %v", err)
	}
}

func TestPermissionFairingRejectsDeniedDecision(t *testing.T) {
	ctx := newPermissionContext(t)
	ctx.Set("current_user_id", "user-7")
	fairing := NewPermissionFairing("pipeline", "trigger", nil)
	fairing.Authorizer = &recordingAuthorizer{allowed: false}

	if err := fairing.OnRequest(ctx); !errors.Is(err, ErrForbidden) {
		t.Fatalf("OnRequest() error = %v, want ErrForbidden", err)
	}
}

func TestPermissionFairingHidesResolverAndAuthorizerErrors(t *testing.T) {
	ctx := newPermissionContext(t)
	ctx.Set("current_user_id", "user-8")

	t.Run("subject resolver", func(t *testing.T) {
		fairing := NewPermissionFairing("secret", "read", nil).SetSubjectResolver(func(*gin.Context) (string, error) {
			return "", errors.New("identity provider detail")
		})
		fairing.Authorizer = &recordingAuthorizer{allowed: true}
		assertInternalPermissionError(t, fairing.OnRequest(ctx))
	})

	t.Run("scope resolver", func(t *testing.T) {
		fairing := NewPermissionFairing("secret", "read", func(*gin.Context) (map[string]string, error) {
			return nil, errors.New("scope storage detail")
		})
		fairing.Authorizer = &recordingAuthorizer{allowed: true}
		assertInternalPermissionError(t, fairing.OnRequest(ctx))
	})

	t.Run("authorizer", func(t *testing.T) {
		fairing := NewPermissionFairing("secret", "read", nil)
		fairing.Authorizer = &recordingAuthorizer{err: errors.New("policy database detail")}
		assertInternalPermissionError(t, fairing.OnRequest(ctx))
	})
}

func TestPermissionFairingRejectsInvalidConfiguration(t *testing.T) {
	ctx := newPermissionContext(t)
	ctx.Set("current_user_id", "user-9")

	for _, fairing := range []*PermissionFairing{
		NewPermissionFairing("", "read", nil),
		NewPermissionFairing("secret", "", nil),
		NewPermissionFairing("secret", "read", nil),
	} {
		if fairing.resource != "" && fairing.action != "" {
			fairing.Authorizer = nil
		} else {
			fairing.Authorizer = &recordingAuthorizer{allowed: true}
		}
		assertInternalPermissionError(t, fairing.OnRequest(ctx))
	}
}

func assertInternalPermissionError(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ErrInternalServer) {
		t.Fatalf("error = %v, want ErrInternalServer", err)
	}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/resources", nil)
	WriteError(ctx, err)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusInternalServerError)
	}
	if body := recorder.Body.String(); body == "" {
		t.Fatal("error response body is empty")
	}
}

func newPermissionContext(t *testing.T) *gin.Context {
	t.Helper()
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/resources", nil)
	return ctx
}

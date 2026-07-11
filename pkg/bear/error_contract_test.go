package bear

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestErrForbiddenWritesHTTP403(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)

	WriteError(ctx, ErrForbidden)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("WriteError(ErrForbidden) status = %d, want 403", recorder.Code)
	}
	if ErrForbidden.Key != "error_forbidden" || ErrForbidden.Status != http.StatusForbidden {
		t.Fatalf("ErrForbidden = %#v, want registered error_forbidden/403", ErrForbidden)
	}
}

package bear

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestCommittedErrorLogsWithoutAppending(t *testing.T) {
	var logs bytes.Buffer
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/value", nil)
	ctx.Set(runtimeContextKey, &Runtime{Logger: slog.New(slog.NewJSONHandler(&logs, nil))})
	ctx.String(http.StatusOK, "already committed")

	WriteError(ctx, errors.New("late failure"))

	if recorder.Code != http.StatusOK || recorder.Body.String() != "already committed" {
		t.Fatalf("response = %d %q", recorder.Code, recorder.Body.String())
	}
	if !ctx.IsAborted() {
		t.Fatal("context was not aborted")
	}
	if !strings.Contains(logs.String(), "Handler execution failed") {
		t.Fatalf("logs = %s", logs.String())
	}
}

func TestCommittedRecoveryLogsAndDoesNotAppend(t *testing.T) {
	var logs bytes.Buffer
	runtime := &Runtime{Logger: slog.New(slog.NewJSONHandler(&logs, nil))}
	router := gin.New()
	router.Use(runtimeRecoveryMiddleware(runtime))
	router.GET("/panic", func(ctx *gin.Context) {
		ctx.String(http.StatusOK, "already committed")
		panic("late failure")
	})

	response := performRequest(router, httptest.NewRequest(http.MethodGet, "/panic", nil))
	if response.Code != http.StatusOK || response.Body.String() != "already committed" {
		t.Fatalf("response = %d %q", response.Code, response.Body.String())
	}
	if !strings.Contains(logs.String(), "Panic recovered") {
		t.Fatalf("logs = %s", logs.String())
	}
}

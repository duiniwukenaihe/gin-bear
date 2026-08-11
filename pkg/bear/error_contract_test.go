package bear

import (
	"bytes"
	"encoding/json"
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

func TestHEADHandlerErrorWritesEmptyBody(t *testing.T) {
	app := Ignite(NewSysConfig())
	app.Handle(http.MethodHead, "/error", func() (string, error) {
		return "", errors.New("handler failure")
	})

	response := performRequest(app, httptest.NewRequest(http.MethodHead, "/error", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}
	if response.Body.Len() != 0 {
		t.Fatalf("body = %q, want empty", response.Body.String())
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

func TestRecoveryMiddlewareHEADPanicWritesEmpty500(t *testing.T) {
	for _, tt := range recoveryMiddlewareCases() {
		t.Run(tt.name, func(t *testing.T) {
			router := newRecoveryContractRouter(tt.middleware)
			router.HEAD("/panic", func(*gin.Context) { panic("head failure") })

			response := performRequest(router, httptest.NewRequest(http.MethodHead, "/panic", nil))
			if response.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
			}
			if response.Body.Len() != 0 {
				t.Fatalf("body = %q, want empty", response.Body.String())
			}
		})
	}
}

func TestRecoveryMiddlewareCommittedPanicDoesNotAppend(t *testing.T) {
	for _, tt := range recoveryMiddlewareCases() {
		t.Run(tt.name, func(t *testing.T) {
			router := newRecoveryContractRouter(tt.middleware)
			router.GET("/panic", func(ctx *gin.Context) {
				ctx.String(http.StatusCreated, "already committed")
				panic("late failure")
			})

			response := performRequest(router, httptest.NewRequest(http.MethodGet, "/panic", nil))
			if response.Code != http.StatusCreated || response.Body.String() != "already committed" {
				t.Fatalf("response = %d %q", response.Code, response.Body.String())
			}
		})
	}
}

func TestRecoveryMiddlewareGETPanicWritesSingleJSON(t *testing.T) {
	for _, tt := range recoveryMiddlewareCases() {
		t.Run(tt.name, func(t *testing.T) {
			router := newRecoveryContractRouter(tt.middleware)
			router.GET("/panic", func(*gin.Context) { panic("get failure") })

			response := performRequest(router, httptest.NewRequest(http.MethodGet, "/panic", nil))
			if response.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
			}
			assertSingleJSONValue(t, response.Body.Bytes())
			var body Response
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Code != http.StatusInternalServerError || body.Message != "Internal Server Error (RID: recovery-rid)" {
				t.Fatalf("body = %#v", body)
			}
		})
	}
}

type recoveryMiddlewareCase struct {
	name       string
	middleware gin.HandlerFunc
}

func recoveryMiddlewareCases() []recoveryMiddlewareCase {
	return []recoveryMiddlewareCase{
		{name: "runtime", middleware: runtimeRecoveryMiddleware(&Runtime{Logger: slog.Default()})},
		{name: "compatibility", middleware: RecoveryMiddleware()},
	}
}

func newRecoveryContractRouter(middleware gin.HandlerFunc) *gin.Engine {
	router := gin.New()
	router.Use(func(ctx *gin.Context) {
		ctx.Set(RequestIDKey, "recovery-rid")
		ctx.Next()
	})
	router.Use(middleware)
	return router
}

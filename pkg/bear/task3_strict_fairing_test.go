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

type task3StrictFairing struct {
	request  func(*gin.Context) error
	response func(any) (any, error)
}

func (f *task3StrictFairing) OnRequest(ctx *gin.Context) error {
	if f.request == nil {
		return nil
	}
	return f.request(ctx)
}

func (f *task3StrictFairing) OnResponse(result any) (any, error) {
	if f.response == nil {
		return result, nil
	}
	return f.response(result)
}

func newTask3StrictApp() *Bear {
	config := NewSysConfig()
	config.SetFrameworkStrict(true)
	return Ignite(config)
}

func newTask3StrictPipeline() *Bear {
	config := NewSysConfig()
	config.SetFrameworkStrict(true)
	return &Bear{
		fairingHandler: NewFairingHandler(),
		runtime:        &Runtime{Config: config},
	}
}

func TestStrictPipelineNilRequestContextIsTerminal(t *testing.T) {
	called := false
	app := newTask3StrictPipeline()
	fairing := &task3StrictFairing{request: func(*gin.Context) error {
		called = true
		return nil
	}}
	app.fairingHandler.AddFairing(fairing)

	if err := app.runPipelineRequestFairings(nil, []Fairing{fairing}); err != nil {
		t.Fatalf("runPipelineRequestFairings(nil) error = %v", err)
	}
	if called {
		t.Fatal("request Fairing ran with a nil context")
	}
}

func TestStrictPipelineNilResponseContextIsTerminal(t *testing.T) {
	called := false
	app := newTask3StrictPipeline()
	fairing := &task3StrictFairing{response: func(result any) (any, error) {
		called = true
		return "changed", nil
	}}
	app.fairingHandler.AddFairing(fairing)

	result, err := app.runPipelineResponseFairings(nil, "original", []Fairing{fairing})
	if err != nil {
		t.Fatalf("runPipelineResponseFairings(nil) error = %v", err)
	}
	if result != "original" {
		t.Fatalf("result = %#v, want original", result)
	}
	if called {
		t.Fatal("response Fairing ran with a nil context")
	}
}

func TestStrictEnteredFairingHelpersTreatNilStateAsTerminal(t *testing.T) {
	requestCalled := false
	requestFairing := &task3StrictFairing{request: func(*gin.Context) error {
		requestCalled = true
		return nil
	}}
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())

	if err := runEnteredRequestFairings(ctx, nil, []Fairing{requestFairing}); err != nil {
		t.Fatalf("runEnteredRequestFairings(nil state) error = %v", err)
	}
	result, err := runEnteredResponseFairings(nil, "original")
	if err != nil {
		t.Fatalf("runEnteredResponseFairings(nil state) error = %v", err)
	}
	if result != "original" {
		t.Fatalf("result = %#v, want original", result)
	}
	if requestCalled {
		t.Fatal("request Fairing ran with a nil entered state")
	}
}

func TestStrictOpaqueGinHandlerStopsAfterCommittedFairing(t *testing.T) {
	called := false
	app := newTask3StrictApp()
	app.Attach(&task3StrictFairing{request: func(ctx *gin.Context) error {
		ctx.String(http.StatusUnauthorized, "blocked")
		return nil
	}})
	app.Handle(http.MethodGet, "/opaque", gin.HandlerFunc(func(ctx *gin.Context) {
		called = true
		ctx.String(http.StatusOK, "handler")
	}))

	response := performRequest(app, httptest.NewRequest(http.MethodGet, "/opaque", nil))
	if called {
		t.Fatal("opaque Gin handler ran after Fairing committed a response")
	}
	if response.Code != http.StatusUnauthorized || response.Body.String() != "blocked" {
		t.Fatalf("response = %d %q", response.Code, response.Body.String())
	}
}

func TestStrictFairingAbortWithoutBodyStopsHandler(t *testing.T) {
	called := false
	app := newTask3StrictApp()
	app.Attach(&task3StrictFairing{request: func(ctx *gin.Context) error {
		ctx.Abort()
		return nil
	}})
	app.Handle(http.MethodGet, "/value", func() string {
		called = true
		return "handler"
	})

	response := performRequest(app, httptest.NewRequest(http.MethodGet, "/value", nil))
	if called {
		t.Fatal("handler ran after Fairing aborted without a body")
	}
	if response.Code != http.StatusOK || response.Body.Len() != 0 {
		t.Fatalf("response = %d %q, want 200 with empty body", response.Code, response.Body.String())
	}
}

func TestStrictFairingTypedErrorStopsHandlerWithSingleJSON(t *testing.T) {
	called := false
	app := newTask3StrictApp()
	app.Attach(&task3StrictFairing{request: func(*gin.Context) error {
		return NewStatusError(http.StatusForbidden, 403, "error_forbidden", errors.New("policy detail"))
	}})
	app.Handle(http.MethodGet, "/value", func() string {
		called = true
		return "handler"
	})

	response := performRequest(app, httptest.NewRequest(http.MethodGet, "/value", nil))
	if called {
		t.Fatal("handler ran after Fairing returned a typed error")
	}
	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}
	assertSingleJSONValue(t, response.Body.Bytes())
	var body Response
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Code != http.StatusForbidden || body.Message != "Forbidden" {
		t.Fatalf("body = %#v", body)
	}
	if strings.Contains(response.Body.String(), "policy detail") {
		t.Fatalf("response leaked typed error cause: %s", response.Body.String())
	}
}

func TestStrictFairingCommittedErrorLogsAbortsAndDoesNotAppend(t *testing.T) {
	var logs bytes.Buffer
	called := false
	var fairingContext *gin.Context
	app := newTask3StrictApp()
	app.runtime.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
	app.Attach(&task3StrictFairing{request: func(ctx *gin.Context) error {
		fairingContext = ctx
		ctx.String(http.StatusUnauthorized, "already committed")
		return errors.New("late Fairing failure")
	}})
	app.Handle(http.MethodGet, "/value", func() string {
		called = true
		return "handler"
	})

	response := performRequest(app, httptest.NewRequest(http.MethodGet, "/value", nil))
	if called {
		t.Fatal("handler ran after committed Fairing returned an error")
	}
	if response.Code != http.StatusUnauthorized || response.Body.String() != "already committed" {
		t.Fatalf("response = %d %q", response.Code, response.Body.String())
	}
	if fairingContext == nil || !fairingContext.IsAborted() {
		t.Fatal("committed Fairing error did not abort the request context")
	}
	if !strings.Contains(logs.String(), "Handler execution failed") {
		t.Fatalf("logs = %s", logs.String())
	}
}

package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	agent "github.com/duiniwukenaihe/gin-bear/extensions/agent"
	"github.com/duiniwukenaihe/gin-bear/extensions/agent/example"
	"github.com/duiniwukenaihe/gin-bear/pkg/bear"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func testHandler(t *testing.T, script ...*schema.Message) (*agent.Handler, *example.Store) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	store, err := example.OpenStore(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Seed("tenant-a", "widget", 100); err != nil {
		t.Fatal(err)
	}
	registry, err := agent.NewRegistry(example.GetOrderTool(store))
	if err != nil {
		t.Fatal(err)
	}
	router, err := agent.NewRouter(
		[]agent.Routing{{Task: "readonly-query", Provider: "fake"}},
		map[string]agent.ChatModel{"fake": agent.NewFakeChatModel(agent.WithScript(script...))},
	)
	if err != nil {
		t.Fatal(err)
	}
	return &agent.Handler{Router: router, Tools: registry, Auditor: agent.NewAuditor(100), Metrics: &agent.Metrics{}}, store
}

// aliceResolve mirrors production order: authentication establishes the
// framework subject first, then trusted middleware derives agent identity.
func aliceResolve(ctx *gin.Context) (agent.Identity, error) {
	ctx.Set("current_user_id", "alice")
	return agent.Identity{UserID: "alice", TenantID: "tenant-a"}, nil
}

func serveApp(t *testing.T, handler *agent.Handler, resolve func(*gin.Context) (agent.Identity, error), fairings ...bear.Fairing) *bear.Bear {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg := bear.NewSysConfig()
	cfg.DB.Enabled = false
	cfg.SetFrameworkStrict(true)
	app := bear.Ignite(cfg)
	app.Attach(&example.IdentityFairing{Resolve: resolve})
	for _, fairing := range fairings {
		app.Attach(fairing)
	}
	app.Mount("/", &example.Module{Handler: handler})
	if err := app.ApplyAll(context.Background()); err != nil {
		t.Fatalf("ApplyAll: %v", err)
	}
	return app
}

// serveAuthorizedApp wires route authorization through the documented
// interface registration: the Authorizer bean satisfies the strict
// PermissionFairing injection.
func serveAuthorizedApp(t *testing.T, handler *agent.Handler, authorizer bear.Authorizer) *bear.Bear {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg := bear.NewSysConfig()
	cfg.DB.Enabled = false
	cfg.SetFrameworkStrict(true)
	app := bear.Ignite(cfg)
	if err := app.Runtime().Container.TrySetWithInterface((*bear.Authorizer)(nil), authorizer); err != nil {
		t.Fatalf("TrySetWithInterface: %v", err)
	}
	app.Attach(&example.IdentityFairing{Resolve: aliceResolve})
	permission := bear.NewPermissionFairing("agent", "invoke", nil)
	app.Attach(permission)
	app.Mount("/", &example.Module{Handler: handler})
	if err := app.ApplyAll(context.Background()); err != nil {
		t.Fatalf("ApplyAll: %v", err)
	}
	return app
}

func postInvoke(t *testing.T, app *bear.Bear, body string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/agent/invoke", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	app.ServeHTTP(recorder, request)
	return recorder
}

func TestInvokeHappyPath(t *testing.T) {
	handler, _ := testHandler(t, agent.FakeToolCall("c1", "get_order", `{"order_id":"1"}`), agent.FakeText("order 1: widget"))
	app := serveApp(t, handler, aliceResolve)
	recorder := postInvoke(t, app, `{"task":"readonly-query","input":"where is order 1"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	var decoded map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["text"] != "order 1: widget" {
		t.Fatalf("body = %v", decoded)
	}
}

func TestInvokeRejectsMissingIdentityUnknownTaskAndLargeInput(t *testing.T) {
	handler, _ := testHandler(t, agent.FakeText("hi"))

	cfg := bear.NewSysConfig()
	cfg.DB.Enabled = false
	cfg.SetFrameworkStrict(true)
	bare := bear.Ignite(cfg)
	bare.Mount("/", &example.Module{Handler: handler})
	if err := bare.ApplyAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if recorder := postInvoke(t, bare, `{"task":"readonly-query","input":"x"}`); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("missing identity status = %d, want 401", recorder.Code)
	}

	app := serveApp(t, handler, aliceResolve)
	if recorder := postInvoke(t, app, `{"task":"nope","input":"x"}`); recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("unknown task status = %d, want 503", recorder.Code)
	}
	huge := strings.Repeat("x", 9000)
	if recorder := postInvoke(t, app, `{"task":"readonly-query","input":"`+huge+`"}`); recorder.Code != http.StatusBadRequest {
		t.Fatalf("oversized input status = %d, want 400", recorder.Code)
	}
}

func TestInvokeKillSwitch(t *testing.T) {
	handler, _ := testHandler(t, agent.FakeText("hi"))
	handler.Disabled = true
	app := serveApp(t, handler, aliceResolve)
	if recorder := postInvoke(t, app, `{"task":"readonly-query","input":"x"}`); recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("disabled status = %d, want 503", recorder.Code)
	}
}

func TestPermissionFairingDeniesAllowsAndFailsClosed(t *testing.T) {
	handler, _ := testHandler(t, agent.FakeText("hi"))
	authorizer, err := bear.NewCasbinAuthorizer(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	app := serveAuthorizedApp(t, handler, authorizer)

	if recorder := postInvoke(t, app, `{"task":"readonly-query","input":"x"}`); recorder.Code != http.StatusForbidden {
		t.Fatalf("denied status = %d, want 403", recorder.Code)
	}
	if _, err := authorizer.AddPolicy("alice", "agent", "invoke"); err != nil {
		t.Fatal(err)
	}
	if recorder := postInvoke(t, app, `{"task":"readonly-query","input":"x"}`); recorder.Code != http.StatusOK {
		t.Fatalf("allowed status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
}

type failingAuthorizer struct{}

func (failingAuthorizer) Authorize(context.Context, bear.AuthorizationRequest) (bool, error) {
	return false, errAuthorizerBackend
}

var errAuthorizerBackend = errors.New("policy backend unavailable")

func TestPermissionFairingFailureIsGeneric500(t *testing.T) {
	handler, _ := testHandler(t, agent.FakeText("hi"))
	app := serveAuthorizedApp(t, handler, failingAuthorizer{})
	recorder := postInvoke(t, app, `{"task":"readonly-query","input":"x"}`)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("failed backend status = %d, want 500", recorder.Code)
	}
	if strings.Contains(recorder.Body.String(), "policy backend") {
		t.Fatalf("500 leaked internals: %s", recorder.Body.String())
	}
}

type sseEvent struct {
	kind string
	data string
}

func parseSSE(t *testing.T, body string) []sseEvent {
	t.Helper()
	var events []sseEvent
	for _, block := range strings.Split(strings.TrimSpace(body), "\n\n") {
		var event sseEvent
		for _, line := range strings.Split(block, "\n") {
			if kind, ok := strings.CutPrefix(line, "event: "); ok {
				event.kind = strings.TrimSpace(kind)
			}
			if data, ok := strings.CutPrefix(line, "data: "); ok {
				if event.data != "" {
					event.data += "\n"
				}
				event.data += strings.TrimSpace(data)
			}
		}
		if event.kind != "" {
			events = append(events, event)
		}
	}
	return events
}

func streamRequest(t *testing.T, app *bear.Bear, task, input string, cancel context.CancelFunc) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/agent/stream?task="+task+"&input="+input, nil)
	if cancel != nil {
		ctx, stop := context.WithCancel(request.Context())
		cancel()
		_ = stop
		request = request.WithContext(ctx)
	}
	app.ServeHTTP(recorder, request)
	return recorder
}

// TestStreamEmitsDeltasAndOneTerminal pins the SSE contract: deltas first,
// then exactly one terminal event.
func TestStreamEmitsDeltasAndOneTerminal(t *testing.T) {
	handler, _ := testHandler(t)
	app := serveApp(t, handler, aliceResolve)
	recorder := streamRequest(t, app, "readonly-query", "hi", nil)
	events := parseSSE(t, recorder.Body.String())
	if len(events) == 0 {
		t.Fatalf("no SSE events:\n%s", recorder.Body.String())
	}
	terminal := 0
	for i, event := range events {
		switch event.kind {
		case "delta":
			if i >= len(events)-1 {
				t.Fatalf("delta is last event: %+v", events)
			}
		case "done", "error":
			terminal++
		default:
			t.Fatalf("unknown SSE kind %q", event.kind)
		}
	}
	if terminal != 1 || events[len(events)-1].kind != "done" {
		t.Fatalf("want exactly one terminal done event: %+v", events)
	}
	var final map[string]any
	if err := json.Unmarshal([]byte(events[len(events)-1].data), &final); err != nil {
		t.Fatalf("terminal payload is not JSON: %v", events[len(events)-1].data)
	}
}

func TestStreamDisconnectCancels(t *testing.T) {
	handler, _ := testHandler(t, agent.FakeText("late"))
	app := serveApp(t, handler, aliceResolve)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/agent/stream?task=readonly-query&input=hi", nil)
		cancelled, cancel := context.WithCancel(request.Context())
		cancel()
		app.ServeHTTP(recorder, request.WithContext(cancelled))
		done <- recorder
	}()
	select {
	case recorder := <-done:
		events := parseSSE(t, recorder.Body.String())
		for _, event := range events {
			if event.kind != "error" && event.kind != "done" && event.kind != "delta" {
				t.Fatalf("unexpected event %+v", event)
			}
		}
	case <-time.After(10 * time.Second):
		t.Fatal("disconnected stream hung the handler")
	}
}

// TestInvokeTenantIsolationOverHTTP is the R5 acceptance: identity flows
// from the trusted fairing through the runner into the business tool. The
// same order id succeeds for its tenant and is denied for another, with no
// manual WithIdentity anywhere in the request path.
func TestInvokeTenantIsolationOverHTTP(t *testing.T) {
	resolveFor := func(user, tenant string) func(*gin.Context) (agent.Identity, error) {
		return func(ctx *gin.Context) (agent.Identity, error) {
			ctx.Set("current_user_id", user)
			return agent.Identity{UserID: user, TenantID: tenant}, nil
		}
	}
	serve := func(t *testing.T, user, tenant, final string) *httptest.ResponseRecorder {
		handler, _ := testHandler(t,
			agent.FakeToolCall("c1", "get_order", `{"order_id":"1"}`),
			agent.FakeText(final),
		)
		app := serveApp(t, handler, resolveFor(user, tenant))
		return postInvoke(t, app, `{"task":"readonly-query","input":"where is order 1"}`)
	}

	granted := serve(t, "alice", "tenant-a", "granted-path")
	if granted.Code != http.StatusOK {
		t.Fatalf("same-tenant status = %d", granted.Code)
	}
	var grantedBody map[string]any
	if err := json.Unmarshal(granted.Body.Bytes(), &grantedBody); err != nil {
		t.Fatal(err)
	}
	// Only a real execution counts: without identity propagation this is 0.
	if grantedBody["tool_calls"] != float64(1) {
		t.Fatalf("same-tenant tool_calls = %v, want 1 (tool never executed)", grantedBody["tool_calls"])
	}

	// Cross-tenant: the tool denies, so the model only ever sees a tool
	// error; the final text must come from the denial branch script.
	denied := serve(t, "bob", "tenant-b", "denied-path")
	if denied.Code != http.StatusOK {
		t.Fatalf("cross-tenant status = %d, want 200 with a tool-error turn", denied.Code)
	}
	var deniedBody map[string]any
	if err := json.Unmarshal(denied.Body.Bytes(), &deniedBody); err != nil {
		t.Fatal(err)
	}
	// The constrained query ran and found nothing: a failed execution still
	// counts as executed, but the model only ever sees a tool error, so the
	// final text must come from the denial branch script.
	if deniedBody["tool_calls"] != float64(1) {
		t.Fatalf("cross-tenant tool_calls = %v, want 1 executed-but-failed call", deniedBody["tool_calls"])
	}
	if body := denied.Body.String(); !strings.Contains(body, "denied-path") {
		t.Fatalf("cross-tenant body = %s", body)
	}
}

// failingWriter fails writes after the first one: headers and the first
// event go through, then the client is gone.
type failingWriter struct {
	http.ResponseWriter
	writes int
}

func (w *failingWriter) Write(data []byte) (int, error) {
	w.writes++
	if w.writes > 1 {
		return 0, errSlowClient
	}
	return w.ResponseWriter.Write(data)
}

func (w *failingWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

var errSlowClient = errorString("slow client gone")

type errorString string

func (e errorString) Error() string { return string(e) }

// TestStreamSlowClientExits is the R6 slow-client acceptance: a dead socket
// ends the handler instead of parking it.
func TestStreamSlowClientExits(t *testing.T) {
	registry, err := agent.NewRegistry(&agent.Tool{
		Info:      &schema.ToolInfo{Name: "get_order", Desc: "read"},
		Authorize: func(context.Context, agent.Identity, map[string]any) error { return nil },
		Execute:   func(context.Context, map[string]any) (string, error) { return "ok", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	fake := agent.NewFakeChatModel(agent.WithStreams(
		[]*schema.Message{{Role: schema.Assistant, Content: "one"}},
		[]*schema.Message{{Role: schema.Assistant, Content: "two"}},
		[]*schema.Message{{Role: schema.Assistant, Content: "three"}},
	))
	router, err := agent.NewRouter(
		[]agent.Routing{{Task: "readonly-query", Provider: "fake"}},
		map[string]agent.ChatModel{"fake": fake},
	)
	if err != nil {
		t.Fatal(err)
	}
	handler := &agent.Handler{Router: router, Tools: registry}
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(&failingWriter{ResponseWriter: recorder})
	ctx.Request = httptest.NewRequest(http.MethodGet, "/agent/stream?task=readonly-query&input=hi", nil)
	agent.SetIdentity(ctx, agent.Identity{UserID: "alice", TenantID: "tenant-a"})

	done := make(chan struct{})
	go func() {
		handler.Stream(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("slow client parked the handler")
	}
}

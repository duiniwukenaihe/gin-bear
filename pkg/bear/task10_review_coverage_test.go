package bear

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

type reviewCoverageFairing struct {
	request  func(*gin.Context) error
	response func(any) (any, error)
}

func (f *reviewCoverageFairing) OnRequest(ctx *gin.Context) error {
	if f.request == nil {
		return nil
	}
	return f.request(ctx)
}

func (f *reviewCoverageFairing) OnResponse(value any) (any, error) {
	if f.response == nil {
		return value, nil
	}
	return f.response(value)
}

type reviewCoverageController struct {
	built atomic.Bool
}

func (c *reviewCoverageController) Name() string { return "reviewCoverageController" }
func (c *reviewCoverageController) Interceptors() []Fairing {
	return []Fairing{&reviewCoverageFairing{}}
}
func (c *reviewCoverageController) Build(app *Bear) {
	c.built.Store(true)
	app.GET("/controller", func(ctx *gin.Context) { ctx.Status(http.StatusNoContent) })
}

type reviewCoverageModule struct {
	controller *reviewCoverageController
}

func (m *reviewCoverageModule) Name() string  { return "reviewCoverageModule" }
func (m *reviewCoverageModule) Beans() []Bean { return nil }
func (m *reviewCoverageModule) Build(app *Bear) {
	app.Mount("/module", m.controller)
}

type reviewContextShutdowner struct{}

func (reviewContextShutdowner) ShutdownContext(ctx context.Context) error { return ctx.Err() }

type reviewWebSocketHandler struct {
	BaseWebSocketHandler
	connected chan struct{}
	messages  chan string
	closed    chan struct{}
}

func (h *reviewWebSocketHandler) OnConnect(*gin.Context, *websocket.Conn) error {
	close(h.connected)
	return nil
}

func (h *reviewWebSocketHandler) OnMessage(_ *gin.Context, conn *websocket.Conn, messageType int, payload []byte) error {
	h.messages <- string(payload)
	return conn.WriteMessage(messageType, append([]byte("echo:"), payload...))
}

func (h *reviewWebSocketHandler) OnClose(*gin.Context, *websocket.Conn) {
	close(h.closed)
}

func TestTask10ReviewExercisesCompleteHandlerResponseFairingAndRoutePipeline(t *testing.T) {
	resetGinModeForTest(t)
	config := NewSysConfig()
	config.DB.Enabled = false
	app := Ignite(config)

	if app.Name() != "Bear" || app.Runtime() == nil || (*Bear)(nil).Runtime() != nil {
		t.Fatal("bear identity or runtime access changed")
	}
	if err := app.EnableIDGenerator(); err != nil || app.EnableMQ(context.Background()) != app {
		t.Fatal("legacy compatibility methods changed")
	}
	WarmupHandlers(nil)

	sequence := make([]string, 0, 4)
	global := &reviewCoverageFairing{
		request: func(*gin.Context) error {
			sequence = append(sequence, "global-request")
			return nil
		},
		response: func(value any) (any, error) {
			sequence = append(sequence, "global-response")
			if text, ok := value.(string); ok {
				return text + "-global", nil
			}
			return value, nil
		},
	}
	route := &reviewCoverageFairing{
		request: func(*gin.Context) error {
			sequence = append(sequence, "route-request")
			return nil
		},
		response: func(value any) (any, error) {
			sequence = append(sequence, "route-response")
			return value.(string) + "-route", nil
		},
	}
	app.Attach(global)
	app.HandleWithFairing(http.MethodGet, "/pipeline", func() string { return "ok" }, route)
	app.Handle(http.MethodGet, "/empty", func() {})
	app.GET("/response", JSONResponse(func(*gin.Context) Response {
		return Result(201, "created", map[string]string{"status": "ok"})
	}).RespondTo())

	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/pipeline", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "ok-global-route") {
		t.Fatalf("pipeline response = %d %s", response.Code, response.Body.String())
	}
	wantSequence := []string{"route-request", "global-request", "global-response", "route-response"}
	if !reflect.DeepEqual(sequence, wantSequence) {
		t.Fatalf("fairing sequence = %v, want %v", sequence, wantSequence)
	}

	response = httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/empty", nil))
	if !strings.Contains(response.Body.String(), `"message":"success"`) {
		t.Fatalf("empty handler response = %s", response.Body.String())
	}
	response = httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/response", nil))
	if !strings.Contains(response.Body.String(), `"code":201`) {
		t.Fatalf("JSONResponse response = %s", response.Body.String())
	}

	writeSuccess(nil, "ignored")
	writtenContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	writtenContext.Status(http.StatusAccepted)
	writeSuccess(writtenContext, "ignored")

	handler := NewFairingHandler()
	requestContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	requestContext.Abort()
	if err := handler.OnRequestWithRoute(requestContext, []Fairing{route}); err != nil {
		t.Fatal(err)
	}
	requestError := errors.New("route request failed")
	activeContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	if err := handler.OnRequestWithRoute(activeContext, []Fairing{&reviewCoverageFairing{request: func(*gin.Context) error { return requestError }}}); !errors.Is(err, requestError) {
		t.Fatalf("route fairing error = %v", err)
	}
}

func TestTask10ReviewExercisesWebSocketRouteLifecycle(t *testing.T) {
	resetGinModeForTest(t)
	config := NewSysConfig()
	config.DB.Enabled = false
	config.Config = make(map[string]any)
	config.Config["websocket.read_timeout"] = "2s"
	config.Config["websocket.write_timeout"] = "1s"
	config.Config["websocket.ping_interval"] = "1h"
	app := Ignite(config)
	handler := &reviewWebSocketHandler{
		connected: make(chan struct{}),
		messages:  make(chan string, 1),
		closed:    make(chan struct{}),
	}
	app.HandleWS("/ws", handler)

	server := httptest.NewServer(app)
	defer server.Close()
	connection, response, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws", nil)
	if err != nil {
		if response != nil {
			t.Fatalf("dial websocket: %v (status %d)", err, response.StatusCode)
		}
		t.Fatalf("dial websocket: %v", err)
	}
	defer connection.Close()

	select {
	case <-handler.connected:
	case <-time.After(time.Second):
		t.Fatal("websocket handler did not receive OnConnect")
	}
	if err := connection.WriteMessage(websocket.TextMessage, []byte("hello")); err != nil {
		t.Fatalf("write websocket message: %v", err)
	}
	messageType, payload, err := connection.ReadMessage()
	if err != nil {
		t.Fatalf("read websocket response: %v", err)
	}
	if messageType != websocket.TextMessage || string(payload) != "echo:hello" {
		t.Fatalf("websocket response = type %d payload %q", messageType, payload)
	}
	select {
	case message := <-handler.messages:
		if message != "hello" {
			t.Fatalf("OnMessage payload = %q", message)
		}
	case <-time.After(time.Second):
		t.Fatal("websocket handler did not receive OnMessage")
	}

	if err := connection.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "done")); err != nil {
		t.Fatalf("close websocket: %v", err)
	}
	select {
	case <-handler.closed:
	case <-time.After(time.Second):
		t.Fatal("websocket handler did not receive OnClose")
	}
}

func TestTask10ReviewExercisesModulesGroupsAndEveryHTTPRouteHelper(t *testing.T) {
	resetGinModeForTest(t)
	config := NewSysConfig()
	config.DB.Enabled = false
	app := Ignite(config)
	controller := &reviewCoverageController{}
	app.AddModule(&reviewCoverageModule{controller: controller})
	if err := app.ApplyAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !controller.built.Load() {
		t.Fatal("module controller was not built")
	}
	if err := app.ApplyAll(context.Background()); err != nil {
		t.Fatal(err)
	}

	handler := func(ctx *gin.Context) { ctx.Status(http.StatusNoContent) }
	app.g = nil
	app.POST("/root-post", handler)
	app.PUT("/root-put", handler)
	app.DELETE("/root-delete", handler)
	app.PATCH("/root-patch", handler)
	app.OPTIONS("/root-options", handler)
	app.HEAD("/root-head", handler)
	app.Any("/root-any", handler)
	app.Group("/root-group", &reviewCoverageController{})

	app.g = app.Engine.Group("/group")
	app.POST("/post", handler)
	app.GET("/get", handler)
	app.PUT("/put", handler)
	app.DELETE("/delete", handler)
	app.PATCH("/patch", handler)
	app.OPTIONS("/options", handler)
	app.HEAD("/head", handler)
	app.Any("/any", handler)
	app.Group("/nested")

	app.pluginMode = true
	app.Handle(http.MethodGet, "/plugin-route", func() string { return "plugin" })
	app.pluginMode = false
	if err := app.ReloadPlugin("disabled.so"); err == nil {
		t.Fatal("disabled plugin reload unexpectedly succeeded")
	}
}

func TestTask10ReviewLaunchesAndShutsDownHTTPAndGRPC(t *testing.T) {
	resetGinModeForTest(t)
	config := NewSysConfig()
	config.DB.Enabled = false
	httpListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	grpcListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		_ = httpListener.Close()
		t.Fatal(err)
	}
	config.Server.Port = int32(httpListener.Addr().(*net.TCPAddr).Port)
	config.GRPC.Enabled = true
	config.GRPC.Port = int32(grpcListener.Addr().(*net.TCPAddr).Port)
	if err := httpListener.Close(); err != nil {
		t.Fatal(err)
	}
	if err := grpcListener.Close(); err != nil {
		t.Fatal(err)
	}
	config.Server.ShutdownTimeout = "2s"
	app := Ignite(config)
	if err := app.AddGRPCServiceE(&grpcServeService{}); err != nil {
		t.Fatalf("register gRPC service: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Launch(ctx) }()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("launch shutdown error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("launch did not stop after cancellation")
	}
}

func TestTask10ReviewExercisesLifecycleNilRepeatAndComparisonBranches(t *testing.T) {
	var lifecycle *Lifecycle
	var nilContext context.Context
	lifecycle.Add(nil)
	lifecycle.setBean(reflect.TypeOf(0), nil)
	lifecycle.removeBean(reflect.TypeOf(0))
	if err := lifecycle.Start(nilContext); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.Stop(nilContext); err != nil {
		t.Fatal(err)
	}

	lifecycle = newLifecycle()
	lifecycle.Add(struct{}{})
	if err := lifecycle.Start(nilContext); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.Stop(nilContext); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}

	if !sameLifecycleComponent(nil, nil) || sameLifecycleComponent(nil, struct{}{}) || sameLifecycleComponent(1, int64(1)) {
		t.Fatal("lifecycle nil or type comparison changed")
	}
	if !sameLifecycleComponent(1, 1) || sameLifecycleComponent(1, 2) {
		t.Fatal("lifecycle comparable value comparison changed")
	}
	left := []int{1}
	right := []int{1}
	if !sameLifecycleComponent(left, left) || sameLifecycleComponent(left, right) {
		t.Fatal("lifecycle reference comparison changed")
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := stopLifecycleComponent(cancelled, reviewContextShutdowner{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("context shutdown error = %v", err)
	}
	if err := stopLifecycleComponent(context.Background(), struct{}{}); err != nil {
		t.Fatalf("plain component shutdown error = %v", err)
	}
}

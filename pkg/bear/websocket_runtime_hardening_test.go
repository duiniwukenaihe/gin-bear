package bear

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

type webSocketConnectionLimitHandler struct {
	BaseWebSocketHandler
	connected chan struct{}
}

type webSocketResponseFairing struct {
	BaseFairing
	requests  atomic.Int32
	responses atomic.Int32
	completed chan struct{}
}

func (f *webSocketResponseFairing) OnRequest(*gin.Context) error {
	f.requests.Add(1)
	return nil
}

func (f *webSocketResponseFairing) OnResponse(result any) (any, error) {
	if f.responses.Add(1) == 1 {
		close(f.completed)
	}
	return result, nil
}

func TestWebSocketRunsGlobalResponseFairingsOnClose(t *testing.T) {
	for _, strict := range []bool{false, true} {
		t.Run(map[bool]string{false: "compatibility", true: "strict"}[strict], func(t *testing.T) {
			resetGinModeForTest(t)
			config := NewSysConfig()
			config.SetFrameworkStrict(strict)
			config.WS.SetAllowedOrigins([]string{"http://example.com"})
			app := Ignite(config)
			fairing := &webSocketResponseFairing{completed: make(chan struct{})}
			if strict {
				if err := app.AttachE(fairing); err != nil {
					t.Fatalf("AttachE() error = %v", err)
				}
			} else {
				app.Attach(fairing)
			}
			app.HandleWS("/ws", &BaseWebSocketHandler{})
			if err := app.ApplyAll(context.Background()); err != nil {
				t.Fatalf("ApplyAll() error = %v", err)
			}

			server := httptest.NewServer(app)
			defer server.Close()
			header := http.Header{"Origin": []string{"http://example.com"}}
			connection, response, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws", header)
			if response != nil {
				defer response.Body.Close()
			}
			if err != nil {
				t.Fatalf("dial WebSocket: %v", err)
			}
			if err := connection.Close(); err != nil {
				t.Fatalf("close WebSocket: %v", err)
			}

			select {
			case <-fairing.completed:
			case <-time.After(time.Second):
				t.Fatal("WebSocket Fairing response callback did not run")
			}
			if requests, responses := fairing.requests.Load(), fairing.responses.Load(); requests != 1 || responses != 1 {
				t.Fatalf("WebSocket Fairing calls = request:%d response:%d, want 1 and 1", requests, responses)
			}
		})
	}
}

func TestWebSocketRejectedHandshakeUnwindsEnteredFairings(t *testing.T) {
	for _, strict := range []bool{false, true} {
		t.Run(map[bool]string{false: "compatibility", true: "strict"}[strict], func(t *testing.T) {
			resetGinModeForTest(t)
			config := NewSysConfig()
			config.SetFrameworkStrict(strict)
			config.WS.SetAllowedOrigins([]string{"http://example.com"})
			app := Ignite(config)
			entered := &webSocketResponseFairing{completed: make(chan struct{})}
			denied := &errorFairing{err: NewStatusError(http.StatusForbidden, http.StatusForbidden, "error_forbidden", nil)}
			if strict {
				if err := app.AttachE(entered, denied); err != nil {
					t.Fatalf("AttachE() error = %v", err)
				}
			} else {
				app.Attach(entered, denied)
			}
			app.HandleWS("/ws", &BaseWebSocketHandler{})
			if err := app.ApplyAll(context.Background()); err != nil {
				t.Fatalf("ApplyAll() error = %v", err)
			}

			server := httptest.NewServer(app)
			defer server.Close()
			header := http.Header{"Origin": []string{"http://example.com"}}
			connection, response, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws", header)
			if connection != nil {
				connection.Close()
				t.Fatal("WebSocket upgraded after a Fairing denied the handshake")
			}
			if err == nil || response == nil {
				t.Fatalf("denied WebSocket dial = response:%v error:%v", response, err)
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusForbidden {
				t.Fatalf("denied WebSocket status = %d, want %d", response.StatusCode, http.StatusForbidden)
			}
			select {
			case <-entered.completed:
			case <-time.After(time.Second):
				t.Fatal("entered WebSocket Fairing did not unwind after handshake rejection")
			}
			if requests, responses := entered.requests.Load(), entered.responses.Load(); requests != 1 || responses != 1 {
				t.Fatalf("entered WebSocket Fairing calls = request:%d response:%d, want 1 and 1", requests, responses)
			}
		})
	}
}

func (h *webSocketConnectionLimitHandler) OnConnect(*gin.Context, *websocket.Conn) error {
	select {
	case h.connected <- struct{}{}:
	default:
	}
	return nil
}

func TestProductionSecurityRejectsAllAddressTrustedProxy(t *testing.T) {
	config := NewSysConfig()
	config.Server.Mode = gin.ReleaseMode
	config.Server.TrustedProxies = []string{"192.0.2.42/0"}
	config.Auth.JWTSecret = "production-jwt-secret-with-more-than-32-random-characters"

	err := validateProductionSecurity(config)
	if err == nil || !strings.Contains(err.Error(), "server.trusted_proxies") {
		t.Fatalf("validateProductionSecurity() error = %v, want trusted proxy rejection", err)
	}
}

func TestStrictWebSocketRouteRequiresOriginAllowlist(t *testing.T) {
	resetGinModeForTest(t)
	for _, origins := range [][]string{nil, {"*"}} {
		config := NewSysConfig()
		config.DB.Enabled = false
		config.SetFrameworkStrict(true)
		config.WS.SetAllowedOrigins(origins)
		app := Ignite(config)
		app.HandleWS("/ws", &BaseWebSocketHandler{})

		err := app.ApplyAll(context.Background())
		if err == nil || !strings.Contains(err.Error(), "websocket.allowed_origins") {
			t.Fatalf("ApplyAll() origins=%v error = %v, want strict WebSocket origin allowlist rejection", origins, err)
		}
	}

	configured := NewSysConfig()
	configured.DB.Enabled = false
	configured.SetFrameworkStrict(true)
	configured.WS.SetAllowedOrigins([]string{"https://app.example.com"})
	app := Ignite(configured)
	app.HandleWS("/ws", &BaseWebSocketHandler{})
	if err := app.ApplyAll(context.Background()); err != nil {
		t.Fatalf("ApplyAll() with an explicit origin: %v", err)
	}
}

func TestWebSocketConnectionLimitRejectsSecondHandshake(t *testing.T) {
	resetGinModeForTest(t)
	config := NewSysConfig()
	config.DB.Enabled = false
	config.Config["websocket.max_connections"] = int64(1)
	app := Ignite(config)
	handler := &webSocketConnectionLimitHandler{connected: make(chan struct{}, 1)}
	app.HandleWS("/ws", handler)

	server := httptest.NewServer(app)
	defer server.Close()
	url := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	first, response, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		if response != nil {
			response.Body.Close()
		}
		t.Fatalf("dial first WebSocket: %v", err)
	}
	defer first.Close()
	select {
	case <-handler.connected:
	case <-time.After(time.Second):
		t.Fatal("first WebSocket did not connect")
	}

	second, response, err := websocket.DefaultDialer.Dial(url, nil)
	if second != nil {
		second.Close()
		t.Fatal("second WebSocket upgraded despite the connection limit")
	}
	if err == nil {
		t.Fatal("second WebSocket dial succeeded despite the connection limit")
	}
	if response == nil {
		t.Fatal("second WebSocket rejection did not include an HTTP response")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("second WebSocket status = %d, want %d", response.StatusCode, http.StatusServiceUnavailable)
	}
	body, readErr := io.ReadAll(response.Body)
	if readErr != nil {
		t.Fatalf("read second WebSocket rejection: %v", readErr)
	}
	var payload Response
	if !json.Valid(body) || json.Unmarshal(body, &payload) != nil || payload.Code != http.StatusServiceUnavailable {
		t.Fatalf("second WebSocket body = %q, want one JSON 503 response", body)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first WebSocket: %v", err)
	}

	deadline := time.Now().Add(time.Second)
	for {
		third, thirdResponse, thirdErr := websocket.DefaultDialer.Dial(url, nil)
		if thirdErr == nil {
			third.Close()
			break
		}
		if thirdResponse != nil {
			thirdResponse.Body.Close()
		}
		if time.Now().After(deadline) {
			t.Fatalf("WebSocket connection slot was not released: %v", thirdErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

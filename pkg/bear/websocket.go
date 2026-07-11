package bear

import (
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

const (
	defaultWebSocketMaxMessageBytes int64 = 1 << 20
	defaultWebSocketReadTimeout           = 60 * time.Second
	defaultWebSocketWriteTimeout          = 10 * time.Second
	defaultWebSocketPingInterval          = 30 * time.Second
)

type webSocketPolicy struct {
	maxMessageBytes int64
	readTimeout     time.Duration
	writeTimeout    time.Duration
	pingInterval    time.Duration
}

func webSocketPolicyForConfig(config *SysConfig) webSocketPolicy {
	policy := webSocketPolicy{
		maxMessageBytes: defaultWebSocketMaxMessageBytes,
		readTimeout:     defaultWebSocketReadTimeout,
		writeTimeout:    defaultWebSocketWriteTimeout,
		pingInterval:    defaultWebSocketPingInterval,
	}
	if config == nil || config.Config == nil {
		return policy
	}
	if value, ok := positiveInt64(config.Config["websocket.max_message_bytes"]); ok {
		policy.maxMessageBytes = value
	}
	if value, ok := positiveDuration(config.Config["websocket.read_timeout"]); ok {
		policy.readTimeout = value
	}
	if value, ok := positiveDuration(config.Config["websocket.write_timeout"]); ok {
		policy.writeTimeout = value
	}
	if value, ok := positiveDuration(config.Config["websocket.ping_interval"]); ok {
		policy.pingInterval = value
	}
	return policy
}

func positiveInt64(value any) (int64, bool) {
	var parsed int64
	switch value := value.(type) {
	case int:
		parsed = int64(value)
	case int32:
		parsed = int64(value)
	case int64:
		parsed = value
	case uint:
		if uint64(value) > uint64(^uint64(0)>>1) {
			return 0, false
		}
		parsed = int64(value)
	case uint64:
		if value > uint64(^uint64(0)>>1) {
			return 0, false
		}
		parsed = int64(value)
	case float64:
		parsed = int64(value)
		if float64(parsed) != value {
			return 0, false
		}
	case string:
		var err error
		parsed, err = strconv.ParseInt(value, 10, 64)
		if err != nil {
			return 0, false
		}
	default:
		return 0, false
	}
	return parsed, parsed > 0
}

func positiveDuration(value any) (time.Duration, bool) {
	switch value := value.(type) {
	case time.Duration:
		return value, value > 0
	case string:
		parsed, err := time.ParseDuration(value)
		return parsed, err == nil && parsed > 0
	default:
		return 0, false
	}
}

func startWebSocketHeartbeat(connection *websocket.Conn, policy webSocketPolicy) func() {
	stop := make(chan struct{})
	done := make(chan struct{})
	var once sync.Once
	go func() {
		defer close(done)
		ticker := time.NewTicker(policy.pingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				deadline := time.Now().Add(policy.writeTimeout)
				if err := connection.WriteControl(websocket.PingMessage, nil, deadline); err != nil {
					return
				}
			case <-stop:
				return
			}
		}
	}()
	return func() {
		once.Do(func() { close(stop) })
		<-done
	}
}

// WebSocketHandler 规范化 WebSocket 事件处理接口
type WebSocketHandler interface {
	OnConnect(ctx *gin.Context, conn *websocket.Conn) error
	OnMessage(ctx *gin.Context, conn *websocket.Conn, messageType int, p []byte) error
	OnClose(ctx *gin.Context, conn *websocket.Conn)
}

// BaseWebSocketHandler 提供接口的默认空实现，方便按需重写
type BaseWebSocketHandler struct {
}

func (h *BaseWebSocketHandler) OnConnect(ctx *gin.Context, conn *websocket.Conn) error {
	return nil
}

func (h *BaseWebSocketHandler) OnMessage(ctx *gin.Context, conn *websocket.Conn, messageType int, p []byte) error {
	return nil
}

func (h *BaseWebSocketHandler) OnClose(ctx *gin.Context, conn *websocket.Conn) {
	_, _ = ctx, conn
}

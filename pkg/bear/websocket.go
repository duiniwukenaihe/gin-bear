package bear

import (
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

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

package bear

import (
	"github.com/gin-gonic/gin"
)

// HealthController 健康检查控制器
type HealthController struct{}

func (h *HealthController) Name() string {
	return "HealthController"
}

func (h *HealthController) Build(bear *Bear) {
	bear.GET("/health", func(ctx *gin.Context) {
		ctx.JSON(200, gin.H{"status": "ok"})
	})
}

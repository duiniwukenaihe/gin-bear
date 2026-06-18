package bear

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

type ReadinessChecker interface {
	Bean
	CheckReady(ctx context.Context) error
}

// HealthController 健康检查控制器
type HealthController struct{}

func (h *HealthController) Name() string {
	return "HealthController"
}

func (h *HealthController) Build(bear *Bear) {
	bear.GET("/health", h.live)
	bear.GET("/live", h.live)
	bear.GET("/ready", h.ready)
}

func (h *HealthController) live(ctx *gin.Context) {
	ctx.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (h *HealthController) ready(ctx *gin.Context) {
	checkCtx, cancel := context.WithTimeout(ctx.Request.Context(), 3*time.Second)
	defer cancel()

	checks := make(map[string]string)
	ready := true
	for _, v := range GetInjector().GetBeanMapper() {
		checker, ok := v.Interface().(ReadinessChecker)
		if !ok {
			continue
		}
		name := checker.Name()
		if err := checker.CheckReady(checkCtx); err != nil {
			ready = false
			checks[name] = err.Error()
			continue
		}
		checks[name] = "ok"
	}

	if !ready {
		ctx.JSON(http.StatusServiceUnavailable, gin.H{
			"status": "not_ready",
			"checks": checks,
		})
		return
	}

	ctx.JSON(http.StatusOK, gin.H{
		"status": "ready",
		"checks": checks,
	})
}

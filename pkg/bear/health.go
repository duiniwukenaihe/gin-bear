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
type HealthController struct {
	runtime *Runtime
}

func (h *HealthController) Name() string {
	return "HealthController"
}

func (h *HealthController) Build(bear *Bear) {
	if h.runtime == nil {
		h.runtime = bear.Runtime()
	}
	bear.GET("/health", h.live)
	bear.GET("/live", h.live)
	bear.GET("/ready", h.ready)
	bear.GET("/version", h.version)
}

func (h *HealthController) live(ctx *gin.Context) {
	ctx.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (h *HealthController) ready(ctx *gin.Context) {
	runtime := h.runtime
	if runtime == nil {
		runtime = defaultRuntime.Load()
	}
	var config *SysConfig
	var container *BeanFactory
	if runtime != nil {
		config = runtime.Config
		container = runtime.Container
	} else {
		container = GetInjector()
		config = Resolve[*SysConfig](container)
	}
	checkCtx, cancel := context.WithTimeout(ctx.Request.Context(), readinessTimeout(config))
	defer cancel()

	checks := make(map[string]string)
	ready := true
	for _, bean := range container.orderedBeans() {
		checker, ok := bean.(ReadinessChecker)
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

func readinessTimeout(config *SysConfig) time.Duration {
	if config == nil || config.Health == nil {
		return 3 * time.Second
	}
	return parseDurationOrDefault(config.Health.ReadinessTimeout, 3*time.Second)
}

func (h *HealthController) version(ctx *gin.Context) {
	ctx.JSON(http.StatusOK, GetBuildInfo())
}

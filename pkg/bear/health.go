package bear

import (
	"context"
	"log/slog"
	"net/http"
	"sort"
	"sync"
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
		runtime = currentDefaultRuntime()
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
	timeout := readinessTimeout(config)
	checkCtx, cancel := context.WithTimeout(ctx.Request.Context(), timeout)
	defer cancel()

	checkers := make([]ReadinessChecker, 0)
	for _, bean := range container.orderedBeans() {
		checker, ok := bean.(ReadinessChecker)
		if !ok {
			continue
		}
		checkers = append(checkers, checker)
	}
	results := runReadinessChecks(checkCtx, timeout, checkers)

	logger := readinessLogger(runtime)
	checks := make(map[string]string, len(results))
	ready := true
	for _, result := range results {
		if result.Err != nil {
			ready = false
			checks[result.Name] = "failed"
			logger.WarnContext(ctx.Request.Context(), "Readiness check failed",
				"check", result.Name,
				"error", result.Err,
			)
			continue
		}
		checks[result.Name] = "ok"
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

type readinessResult struct {
	Name string
	Err  error
}

func runReadinessChecks(parent context.Context, timeout time.Duration, checkers []ReadinessChecker) []readinessResult {
	if len(checkers) == 0 {
		return nil
	}
	sort.Slice(checkers, func(i, j int) bool {
		return checkers[i].Name() < checkers[j].Name()
	})

	results := make([]readinessResult, len(checkers))
	var wg sync.WaitGroup
	wg.Add(len(checkers))
	for i, checker := range checkers {
		i, checker := i, checker
		go func() {
			defer wg.Done()
			childCtx, cancel := context.WithTimeout(parent, timeout)
			defer cancel()
			results[i] = readinessResult{
				Name: checker.Name(),
				Err:  checker.CheckReady(childCtx),
			}
		}()
	}
	wg.Wait()
	return results
}

func readinessLogger(runtime *Runtime) *slog.Logger {
	if runtime != nil && runtime.Logger != nil {
		return runtime.Logger
	}
	return slog.Default()
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

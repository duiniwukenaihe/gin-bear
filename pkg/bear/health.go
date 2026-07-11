package bear

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"reflect"
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
	results := runReadinessChecksWithCoordinator(checkCtx, timeout, checkers, runtimeReadinessChecks(runtime))

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

var errReadinessCheckBusy = errors.New("readiness check already in progress")

type readinessCheckerKey struct {
	typ     reflect.Type
	pointer uintptr
	name    string
}

type readinessCheckCoordinator struct {
	mu       sync.Mutex
	inFlight map[readinessCheckerKey]struct{}
}

func newReadinessCheckCoordinator() *readinessCheckCoordinator {
	return &readinessCheckCoordinator{inFlight: make(map[readinessCheckerKey]struct{})}
}

func runtimeReadinessChecks(runtime *Runtime) *readinessCheckCoordinator {
	if runtime != nil && runtime.readinessChecks != nil {
		return runtime.readinessChecks
	}
	return newReadinessCheckCoordinator()
}

func runReadinessChecks(parent context.Context, timeout time.Duration, checkers []ReadinessChecker) []readinessResult {
	return runReadinessChecksWithCoordinator(parent, timeout, checkers, newReadinessCheckCoordinator())
}

func runReadinessChecksWithCoordinator(parent context.Context, timeout time.Duration, checkers []ReadinessChecker, coordinator *readinessCheckCoordinator) []readinessResult {
	if len(checkers) == 0 {
		return nil
	}
	if coordinator == nil {
		coordinator = newReadinessCheckCoordinator()
	}
	ordered := append([]ReadinessChecker(nil), checkers...)
	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i].Name() < ordered[j].Name()
	})

	deadlineCtx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	type indexedResult struct {
		index int
		readinessResult
	}
	completed := make(chan indexedResult, len(ordered))
	for i, checker := range ordered {
		if coordinator.start(checker) {
			i, checker := i, checker
			go func() {
				result := readinessResult{Name: checker.Name(), Err: checker.CheckReady(deadlineCtx)}
				coordinator.finish(checker)
				completed <- indexedResult{index: i, readinessResult: result}
			}()
			continue
		}
		completed <- indexedResult{
			index:           i,
			readinessResult: readinessResult{Name: checker.Name(), Err: errReadinessCheckBusy},
		}
	}

	results := make([]readinessResult, len(ordered))
	finished := make([]bool, len(ordered))
	for remaining := len(ordered); remaining > 0; {
		select {
		case result := <-completed:
			if !finished[result.index] {
				results[result.index] = result.readinessResult
				finished[result.index] = true
				remaining--
			}
		case <-deadlineCtx.Done():
			for i, checker := range ordered {
				if !finished[i] {
					results[i] = readinessResult{Name: checker.Name(), Err: deadlineCtx.Err()}
				}
			}
			return results
		}
	}
	return results
}

func (c *readinessCheckCoordinator) start(checker ReadinessChecker) bool {
	key := readinessCheckerIdentity(checker)
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, running := c.inFlight[key]; running {
		return false
	}
	c.inFlight[key] = struct{}{}
	return true
}

func (c *readinessCheckCoordinator) finish(checker ReadinessChecker) {
	c.mu.Lock()
	delete(c.inFlight, readinessCheckerIdentity(checker))
	c.mu.Unlock()
}

func readinessCheckerIdentity(checker ReadinessChecker) readinessCheckerKey {
	value := reflect.ValueOf(checker)
	key := readinessCheckerKey{typ: value.Type()}
	if value.Kind() == reflect.Ptr {
		key.pointer = value.Pointer()
		return key
	}
	key.name = checker.Name()
	return key
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

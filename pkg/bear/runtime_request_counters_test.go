package bear

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestRuntimeRequestCountersArePerRuntime 固定请求/错误计数的按运行时隔离。
// 进程级 TotalRequests/TotalErrors 会把同一进程内多个 Bear 实例的流量混在一起，
// Runtime.Requests/Errors 只统计自己这个运行时。
func TestRuntimeRequestCountersArePerRuntime(t *testing.T) {
	resetGinModeForTest(t)

	first := Ignite(NewSysConfig())
	second := Ignite(NewSysConfig())
	t.Cleanup(func() {
		_ = first.Shutdown(context.Background())
		_ = second.Shutdown(context.Background())
	})

	first.GET("/ok", func(ctx *gin.Context) { ctx.Status(http.StatusNoContent) })
	first.GET("/boom", func(ctx *gin.Context) { ctx.Status(http.StatusInternalServerError) })
	second.GET("/ok", func(ctx *gin.Context) { ctx.Status(http.StatusNoContent) })

	baselineRequests := atomic.LoadInt64(&TotalRequests)
	baselineErrors := atomic.LoadInt64(&TotalErrors)

	for range 3 {
		performRequest(first.Engine, httptest.NewRequest(http.MethodGet, "/ok", nil))
	}
	performRequest(first.Engine, httptest.NewRequest(http.MethodGet, "/boom", nil))
	performRequest(second.Engine, httptest.NewRequest(http.MethodGet, "/ok", nil))

	if got := first.Runtime().Requests(); got != 4 {
		t.Fatalf("first runtime requests = %d, want 4", got)
	}
	if got := second.Runtime().Requests(); got != 1 {
		t.Fatalf("second runtime requests = %d, want 1", got)
	}
	if got := first.Runtime().Errors(); got != 1 {
		t.Fatalf("first runtime errors = %d, want 1", got)
	}
	if got := second.Runtime().Errors(); got != 0 {
		t.Fatalf("second runtime errors = %d, want 0", got)
	}

	// 进程级计数器仍然把两个运行时的流量加在一起，这正是它被标记废弃的原因。
	if got := atomic.LoadInt64(&TotalRequests) - baselineRequests; got != 5 {
		t.Fatalf("process-wide TotalRequests delta = %d, want 5", got)
	}
	if got := atomic.LoadInt64(&TotalErrors) - baselineErrors; got != 1 {
		t.Fatalf("process-wide TotalErrors delta = %d, want 1", got)
	}
}

// TestRuntimeRequestCountersSurviveConcurrentTraffic 确认新计数器可被并发读取。
func TestRuntimeRequestCountersSurviveConcurrentTraffic(t *testing.T) {
	resetGinModeForTest(t)

	app := Ignite(NewSysConfig())
	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })
	app.GET("/ok", func(ctx *gin.Context) { ctx.Status(http.StatusNoContent) })

	const requests = 64
	var group sync.WaitGroup
	for range requests {
		group.Add(1)
		go func() {
			defer group.Done()
			performRequest(app.Engine, httptest.NewRequest(http.MethodGet, "/ok", nil))
		}()
	}
	group.Add(1)
	go func() {
		defer group.Done()
		for range requests {
			_ = app.Runtime().Requests()
			_ = app.Runtime().Errors()
		}
	}()
	group.Wait()

	if got := app.Runtime().Requests(); got != requests {
		t.Fatalf("runtime requests = %d, want %d", got, requests)
	}
}

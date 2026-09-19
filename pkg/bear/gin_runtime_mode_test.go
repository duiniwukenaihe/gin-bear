package bear

import (
	"context"
	"errors"
	"testing"

	"github.com/gin-gonic/gin"
)

func strictModeConfig(mode string) *SysConfig {
	config := NewSysConfig()
	config.SetFrameworkStrict(true)
	config.Server.Mode = mode
	return config
}

func newStrictModeApp(t *testing.T, mode string) *Bear {
	t.Helper()
	app, err := IgniteE(strictModeConfig(mode))
	if err != nil {
		t.Fatalf("IgniteE(mode=%s) error = %v", mode, err)
	}
	return app
}

func shutdownStrictApp(t *testing.T, app *Bear) {
	t.Helper()
	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
}

// TestStrictGinModeIsReleasedAfterShutdown 覆盖进程级 gin 模式锁的释放：
// 上一个 strict 运行时已经关闭之后，新建一个模式不同的 strict 运行时不应再被
// 判定为模式冲突。修复前 strictGinRuntimeMode 只置不复位，这里会失败。
func TestStrictGinModeIsReleasedAfterShutdown(t *testing.T) {
	resetGinModeForTest(t)

	first := newStrictModeApp(t, gin.DebugMode)
	shutdownStrictApp(t, first)

	second, err := IgniteE(strictModeConfig(gin.ReleaseMode))
	if err != nil {
		t.Fatalf("IgniteE() after previous strict runtime shut down error = %v, want nil", err)
	}
	shutdownStrictApp(t, second)
}

// TestStrictGinModeConflictIsScopedToLiveRuntimes 确认模式锁本身仍然生效：
// 只要还有一个 strict 运行时存活，请求不同模式就必须继续被拒绝。
func TestStrictGinModeConflictIsScopedToLiveRuntimes(t *testing.T) {
	resetGinModeForTest(t)

	live := newStrictModeApp(t, gin.DebugMode)
	defer shutdownStrictApp(t, live)

	if _, err := IgniteE(strictModeConfig(gin.ReleaseMode)); !errors.Is(err, ErrGinRuntimeConflict) {
		t.Fatalf("IgniteE(conflicting mode) error = %v, want ErrGinRuntimeConflict", err)
	}
}

// TestStrictGinModeStaysHeldWhileAnyRuntimeLives 固定引用计数语义：
// 多个同模式 strict 运行时共存时，关闭其中一个不得提前释放模式锁。
func TestStrictGinModeStaysHeldWhileAnyRuntimeLives(t *testing.T) {
	resetGinModeForTest(t)

	first := newStrictModeApp(t, gin.DebugMode)
	second := newStrictModeApp(t, gin.DebugMode)

	shutdownStrictApp(t, first)

	if _, err := IgniteE(strictModeConfig(gin.ReleaseMode)); !errors.Is(err, ErrGinRuntimeConflict) {
		t.Fatalf("conflicting mode accepted while another strict runtime is live: %v", err)
	}

	shutdownStrictApp(t, second)

	third, err := IgniteE(strictModeConfig(gin.ReleaseMode))
	if err != nil {
		t.Fatalf("IgniteE() after all strict runtimes shut down error = %v, want nil", err)
	}
	shutdownStrictApp(t, third)
}

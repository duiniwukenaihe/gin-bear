package bear

import (
	"context"
	"sync"
	"testing"
)

// TestDefaultFacadeFallsBackToSupersededRuntime 固定"运行时停止后兼容门面必须退回到被它
// 顶替的那个运行时"这一行为。
//
// 进程级门面（包级 helper 如 GetByType / Log 解析用的那个）只有一个槽位。旧实现 publish
// 时直接覆盖，运行时停止后门面仍然指着已经关掉的运行时，包级 helper 于是永远拿到一个死容器
// 或死 logger。现在 publish 会把被顶替的门面压栈，读取时发现当前门面所属运行时已停止，就按
// 后进先出退回到仍然活着的那个。
func TestDefaultFacadeFallsBackToSupersededRuntime(t *testing.T) {
	resetDefaultFacade()
	t.Cleanup(resetDefaultFacade)

	first := newRuntime(NewSysConfig())
	second := newRuntime(NewSysConfig())
	if first.Lifecycle.stopped() || second.Lifecycle.stopped() {
		t.Fatal("freshly created runtimes must not report themselves as stopped")
	}

	publishDefaultRuntime(first)
	if got := currentDefaultRuntime(); got != first {
		t.Fatalf("after publishing first runtime, default runtime = %p, want %p", got, first)
	}

	publishDefaultRuntime(second)
	if got := currentDefaultRuntime(); got != second {
		t.Fatalf("after publishing second runtime, default runtime = %p, want %p", got, second)
	}

	// 较新的运行时停止后，门面必须退回到被它顶替的那个，而不是继续指向死运行时。
	if err := second.Lifecycle.Stop(context.Background()); err != nil {
		t.Fatalf("stop second runtime: %v", err)
	}
	facade := loadDefaultFacade()
	if facade == nil {
		t.Fatal("facade = nil after the newest runtime stopped, want the superseded runtime")
	}
	if facade.runtime != first {
		t.Fatalf("facade runtime = %p, want superseded runtime %p", facade.runtime, first)
	}
	if facade.injector != first.Container || facade.logger != first.Logger {
		t.Fatalf("facade = %#v, want a coherent snapshot of the superseded runtime", facade)
	}
	if got := currentDefaultRuntime(); got != first {
		t.Fatalf("currentDefaultRuntime() = %p, want %p", got, first)
	}

	// 最后一个运行时也停止后，门面清空，而不是继续指着任何已停止的运行时。
	if err := first.Lifecycle.Stop(context.Background()); err != nil {
		t.Fatalf("stop first runtime: %v", err)
	}
	if facade := loadDefaultFacade(); facade != nil {
		t.Fatalf("facade = %#v after every runtime stopped, want nil", facade)
	}
	if got := currentDefaultRuntime(); got != nil {
		t.Fatalf("currentDefaultRuntime() = %p after every runtime stopped, want nil", got)
	}
}

// TestDefaultFacadeSkipsSupersededRuntimeThatAlreadyStopped 覆盖"被顶替者本身也已经停止"
// 的情况：此时不能把死运行时恢复成默认门面，只能继续清空。
func TestDefaultFacadeSkipsSupersededRuntimeThatAlreadyStopped(t *testing.T) {
	resetDefaultFacade()
	t.Cleanup(resetDefaultFacade)

	first := newRuntime(NewSysConfig())
	second := newRuntime(NewSysConfig())

	publishDefaultRuntime(first)
	publishDefaultRuntime(second)

	// 先停掉被顶替的那个，栈里留下的是一条无法恢复的记录。
	if err := first.Lifecycle.Stop(context.Background()); err != nil {
		t.Fatalf("stop first runtime: %v", err)
	}
	if err := second.Lifecycle.Stop(context.Background()); err != nil {
		t.Fatalf("stop second runtime: %v", err)
	}

	if facade := loadDefaultFacade(); facade != nil {
		t.Fatalf("facade = %#v, want nil because the superseded runtime is also stopped", facade)
	}
}

// TestPublishDefaultRuntimeAfterShutdownDoesNotStackDeadFacade 确认反复"点火—关闭"不会让
// 死门面在栈里堆积，也不会把死运行时重新当成默认门面。
func TestPublishDefaultRuntimeAfterShutdownDoesNotStackDeadFacade(t *testing.T) {
	resetDefaultFacade()
	t.Cleanup(resetDefaultFacade)

	dead := newRuntime(NewSysConfig())
	publishDefaultRuntime(dead)
	if err := dead.Lifecycle.Stop(context.Background()); err != nil {
		t.Fatalf("stop dead runtime: %v", err)
	}

	live := newRuntime(NewSysConfig())
	publishDefaultRuntime(live)

	defaultFacadeMu.Lock()
	stacked := len(supersededFacades)
	defaultFacadeMu.Unlock()
	if stacked != 0 {
		t.Fatalf("superseded facades = %d, want 0: a stopped facade must not be remembered", stacked)
	}
	if got := currentDefaultRuntime(); got != live {
		t.Fatalf("currentDefaultRuntime() = %p, want %p", got, live)
	}

	// live 停止后栈里没有可退的东西，门面必须清空而不是退回 dead。
	if err := live.Lifecycle.Stop(context.Background()); err != nil {
		t.Fatalf("stop live runtime: %v", err)
	}
	if facade := loadDefaultFacade(); facade != nil {
		t.Fatalf("facade = %#v, want nil", facade)
	}
}

// TestLegacyLoggerTargetLeavesStoppedRuntime 确认日志这种每次写记录都会走的路径，在运行时
// 停止后不再把记录送进已经关掉的运行时的 handler，而是回退到 bootstrap logger。
func TestLegacyLoggerTargetLeavesStoppedRuntime(t *testing.T) {
	resetDefaultFacade()
	t.Cleanup(resetDefaultFacade)

	runtime := newRuntime(NewSysConfig())
	publishDefaultRuntime(runtime)

	if got := legacyLoggerTarget(); got != runtime.Logger {
		t.Fatalf("legacyLoggerTarget() = %p, want live runtime logger %p", got, runtime.Logger)
	}

	if err := runtime.Lifecycle.Stop(context.Background()); err != nil {
		t.Fatalf("stop runtime: %v", err)
	}

	if got := legacyLoggerTarget(); got != bootstrapLogger {
		t.Fatalf("legacyLoggerTarget() = %p after shutdown, want bootstrap logger %p", got, bootstrapLogger)
	}
}

// TestDefaultFacadeReconcileIsRaceFreeUnderConcurrentReaders 确认门面的快速路径无锁，且与
// "运行时停止"并发时不会死锁、不产生数据竞争。读线程会同时经历快路径（门面存活）与慢路径
// （门面已停止后的对账）。
func TestDefaultFacadeReconcileIsRaceFreeUnderConcurrentReaders(t *testing.T) {
	resetDefaultFacade()
	t.Cleanup(resetDefaultFacade)

	superseded := newRuntime(NewSysConfig())
	current := newRuntime(NewSysConfig())
	publishDefaultRuntime(superseded)
	publishDefaultRuntime(current)

	const readers = 8
	start := make(chan struct{})
	var group sync.WaitGroup
	group.Add(readers)
	for range readers {
		go func() {
			defer group.Done()
			<-start
			for range 1_000 {
				_ = loadDefaultFacade()
			}
		}()
	}

	close(start)
	// 读线程正在读的时候把当前运行时停掉，强制它们走对账路径。
	if err := current.Lifecycle.Stop(context.Background()); err != nil {
		t.Fatalf("stop current runtime: %v", err)
	}
	group.Wait()

	if got := currentDefaultRuntime(); got != superseded {
		t.Fatalf("currentDefaultRuntime() = %p, want superseded runtime %p", got, superseded)
	}
}

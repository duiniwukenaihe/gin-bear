package gen

import (
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

// missingWatcherBinary 保证 cmd.Start 立即失败，测试因此不会真的拉起子进程，
// 但仍会完整走过 Restart 对 w.cmd 的赋值路径。
const missingWatcherBinary = "gin-bear-watcher-test-missing-binary"

type watcherCommandRecorder struct {
	mu   sync.Mutex
	cmds []*exec.Cmd
}

func (r *watcherCommandRecorder) factory() *exec.Cmd {
	cmd := exec.Command(missingWatcherBinary)
	r.mu.Lock()
	r.cmds = append(r.cmds, cmd)
	r.mu.Unlock()
	return cmd
}

func (r *watcherCommandRecorder) created() []*exec.Cmd {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*exec.Cmd(nil), r.cmds...)
}

func swapWatcherCommand(t *testing.T, factory func() *exec.Cmd) {
	t.Helper()
	original := newWatcherCommand
	newWatcherCommand = factory
	t.Cleanup(func() { newWatcherCommand = original })
}

// TestWatcherRestartSerialisesCommandOwnership 覆盖 Restart 的并发调用：
// w.cmd 的赋值与读取必须处于同一把锁之下，否则 -race 会报告数据竞争。
func TestWatcherRestartSerialisesCommandOwnership(t *testing.T) {
	recorder := &watcherCommandRecorder{}
	swapWatcherCommand(t, recorder.factory)

	dir := t.TempDir()
	w := NewWatcher(dir)

	const workers, restarts = 4, 8
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			for range restarts {
				w.Restart()
			}
		}()
	}
	group.Wait()

	created := recorder.created()
	if len(created) != workers*restarts {
		t.Fatalf("started commands = %d, want %d", len(created), workers*restarts)
	}
	for index, cmd := range created {
		if cmd.Dir != dir {
			t.Fatalf("command %d Dir = %q, want %q", index, cmd.Dir, dir)
		}
	}
	if w.cmd != nil {
		t.Fatalf("w.cmd = %v, want nil after every Start failed", w.cmd)
	}
}

// TestWatcherStartReturnsWhenWatchStreamCloses 覆盖监听流意外关闭的场景：
// Start 必须返回而不是永久阻塞在 done 上。
func TestWatcherStartReturnsWhenWatchStreamCloses(t *testing.T) {
	recorder := &watcherCommandRecorder{}
	swapWatcherCommand(t, recorder.factory)

	stream, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatalf("fsnotify.NewWatcher() error = %v", err)
	}
	original := newFileWatcher
	newFileWatcher = func() (*fsnotify.Watcher, error) { return stream, nil }
	t.Cleanup(func() { newFileWatcher = original })

	w := NewWatcher(t.TempDir())
	started := make(chan struct{})
	go func() {
		defer close(started)
		w.Start()
	}()

	if err := stream.Close(); err != nil {
		t.Fatalf("stream.Close() error = %v", err)
	}

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("Start() blocked forever after the watch stream closed")
	}
}

// TestLoadConfigForCLIReadsTargetDir 锁定配置解析不再依赖进程 CWD 的行为：
// 配置文件必须从传入目录读取，且调用前后进程工作目录保持不变。
func TestLoadConfigForCLIReadsTargetDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "application.yaml"), []byte("server:\n  port: 18081\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	before, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	config := LoadConfigForCLI(dir)
	after, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	if config == nil {
		t.Fatal("LoadConfigForCLI() returned nil config")
	}
	if config.Server.Port != 18081 {
		t.Fatalf("server port = %d, want 18081 (config not read from %s)", config.Server.Port, dir)
	}
	if before != after {
		t.Fatalf("working directory changed from %q to %q", before, after)
	}
}

// TestLoadConfigForCLIMissingFileFallsBackToDefaults 确认目录中没有配置文件时返回默认配置。
func TestLoadConfigForCLIMissingFileFallsBackToDefaults(t *testing.T) {
	config := LoadConfigForCLI(t.TempDir())
	if config == nil {
		t.Fatal("LoadConfigForCLI() returned nil config")
	}
	if config.Server.Port != 8080 {
		t.Fatalf("server port = %d, want default 8080", config.Server.Port)
	}
}

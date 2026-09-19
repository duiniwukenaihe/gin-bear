package gen

import (
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/duiniwukenaihe/gin-bear/pkg/bear"
)

// newWatcherCommand 构造被监听器重启的进程。声明为变量，便于测试注入廉价命令，
// 避免单元测试真的执行 go run。
var newWatcherCommand = func() *exec.Cmd {
	return exec.Command("go", "run", "cmd/main.go")
}

// newFileWatcher 构造文件系统监听器，同样可被测试替换。
var newFileWatcher = fsnotify.NewWatcher

// LoadConfigForCLI 尝试加载配置，用于 CLI 决策
func LoadConfigForCLI(dir string) *bear.SysConfig {
	// 只读取目标目录下的配置文件，不切换进程工作目录：os.Chdir 是进程级全局状态，
	// 并发调用、或调用期间其他依赖相对路径的代码都会受到干扰。
	config := bear.NewSysConfig()
	yamlFile := filepath.Join(dir, "application.yaml")
	if _, err := os.Stat(yamlFile); err == nil {
		if err := bear.ParseConfig(yamlFile, config); err != nil {
			log.Printf("Failed to parse %s: %v", yamlFile, err)
		}
	}
	return config
}

// RunOnce 仅运行一次，不监听
func RunOnce(dir string) {
	cmd := newWatcherCommand()
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Printf("Process exited with error: %v", err)
	}
}

type Watcher struct {
	Dir string
	cmd *exec.Cmd
	mu  sync.Mutex
}

func NewWatcher(dir string) *Watcher {
	return &Watcher{Dir: dir}
}

func (w *Watcher) Start() {
	watcher, err := newFileWatcher()
	if err != nil {
		log.Fatal(err)
	}
	defer watcher.Close()

	// done 在事件循环退出时关闭，使 Start 能返回并释放监听器。
	// 监听流若意外关闭，事件循环随之结束，Start 不再永久阻塞。
	done := make(chan struct{})
	go func() {
		defer close(done)
		// lastRun 只被本协程访问，因此无需加锁，也不再是 Watcher 的共享字段。
		var lastRun time.Time
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				if event.Op&fsnotify.Write == fsnotify.Write {
					ext := filepath.Ext(event.Name)
					if ext == ".go" || ext == ".yaml" || ext == ".yml" {
						// 防抖处理：500ms 内只重启一次
						if time.Since(lastRun) > 500*time.Millisecond {
							log.Printf("File changed: %s, restarting...", event.Name)
							w.Restart()
							lastRun = time.Now()
						}
					}
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				log.Println("error:", err)
			}
		}
	}()

	// 监听子目录
	walkErr := filepath.Walk(w.Dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || !info.IsDir() {
			return nil
		}
		if isSkippedWatchDir(path) {
			// 跳过 .git / vendor 整棵子树：既不监听，也不再向下遍历。
			return filepath.SkipDir
		}
		if err := watcher.Add(path); err != nil {
			// 单个目录监听失败不应中断整棵目录树的监听。
			log.Printf("Failed to watch %s: %v", path, err)
		}
		return nil
	})
	if walkErr != nil {
		log.Printf("Failed to walk %s: %v", w.Dir, walkErr)
	}

	// 初始启动
	w.Restart()
	<-done
}

func isSkippedWatchDir(path string) bool {
	base := filepath.Base(path)
	return base == ".git" || base == "vendor"
}

func (w *Watcher) Restart() {
	w.mu.Lock()
	defer w.mu.Unlock()

	// 1. 杀掉旧进程
	if w.cmd != nil && w.cmd.Process != nil {
		log.Println("Stopping old process...")
		w.cmd.Process.Kill()
		w.cmd.Wait()
	}

	// 2. 重新编译并启动 (这里简化处理，假设 main.go 在 cmd/main.go)
	// cmd.Start 只等待 fork/exec 完成，编译发生在子进程内部，因此持锁时间很短。
	// cmd.Dir 必须显式指定：重启的进程要在被监听的目录下运行，而不是依赖进程 CWD。
	cmd := newWatcherCommand()
	cmd.Dir = w.Dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	w.cmd = cmd
	if err := cmd.Start(); err != nil {
		log.Printf("Failed to start process: %v", err)
		w.cmd = nil
	}
}

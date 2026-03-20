package gen

import (
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"bear/pkg/bear"

	"github.com/fsnotify/fsnotify"
)

// LoadConfigForCLI 尝试加载配置，用于 CLI 决策
func LoadConfigForCLI(dir string) *bear.SysConfig {
	// 切换到目标目录并尝试加载
	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	// 仅尝试加载文件，不触发初始化逻辑
	config := bear.NewSysConfig()
	yamlFile := "application.yaml"
	if _, err := os.Stat(yamlFile); err == nil {
		bear.ParseConfig(yamlFile, config)
	}
	return config
}

// RunOnce 仅运行一次，不监听
func RunOnce(dir string) {
	cmd := exec.Command("go", "run", "cmd/main.go")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Printf("Process exited with error: %v", err)
	}
}

type Watcher struct {
	Dir      string
	cmd      *exec.Cmd
	mu       sync.Mutex
	lastRun  time.Time
}

func NewWatcher(dir string) *Watcher {
	return &Watcher{Dir: dir}
}

func (w *Watcher) Start() {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Fatal(err)
	}
	defer watcher.Close()

	done := make(chan bool)
	go func() {
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
						if time.Since(w.lastRun) > 500*time.Millisecond {
							log.Printf("File changed: %s, restarting...", event.Name)
							w.Restart()
							w.lastRun = time.Now()
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
	filepath.Walk(w.Dir, func(path string, info os.FileInfo, err error) error {
		if info != nil && info.IsDir() {
			if strings := path; !contains(strings, ".git") && !contains(strings, "vendor") {
				return watcher.Add(path)
			}
		}
		return nil
	})

	// 初始启动
	w.Restart()
	<-done
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
	// 在实际应用中，用户可能需要指定 entry point
	go func() {
		cmd := exec.Command("go", "run", "cmd/main.go")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		w.cmd = cmd
		if err := cmd.Start(); err != nil {
			log.Printf("Failed to start process: %v", err)
		}
	}()
}

func contains(path string, sub string) bool {
	return filepath.Base(path) == sub
}

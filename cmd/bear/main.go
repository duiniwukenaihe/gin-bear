package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"bear/pkg/bear/gen"
)

func usage() {
	fmt.Println("Usage: bear <command> [arguments]")
	fmt.Println("\nCommands:")
	fmt.Println("  new <name>              Create a new gin-bear project")
	fmt.Println("  run                     Run the project with hot-reloading")
	fmt.Println("  gen ioc                 Generate static DI code")
	fmt.Println("  gen controller <name>   Generate a new controller")
	fmt.Println("  gen service <name>      Generate a new service")
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "new":
		if len(os.Args) < 3 {
			log.Fatal("Project name is required")
		}
		projectName := os.Args[2]
		createProject(projectName)

	case "run":
		runCmd := flag.NewFlagSet("run", flag.ExitOnError)
		noReload := runCmd.Bool("no-reload", false, "disable hot-reloading regardless of config")
		runCmd.Parse(os.Args[2:])

		// 预加载配置以读取热更新开关
		config := gen.LoadConfigForCLI(".")
		shouldReload := true
		if config != nil && config.Server != nil {
			shouldReload = config.Server.HotReload
		}
		if *noReload {
			shouldReload = false
		}

		if shouldReload {
			watcher := gen.NewWatcher(".")
			watcher.Start()
		} else {
			fmt.Println("Hot-reloading is disabled. Running once...")
			gen.RunOnce(".")
		}

	case "gen":
		if len(os.Args) < 3 {
			usage()
			os.Exit(1)
		}
		handleGen(os.Args[2:])

	default:
		usage()
		os.Exit(1)
	}
}

func createProject(name string) {
	fmt.Printf("Creating new project: %s...\n", name)
	dirs := []string{
		name + "/cmd",
		name + "/pkg/controllers",
		name + "/pkg/services",
		name + "/pkg/models",
		name + "/configs",
	}

	for _, d := range dirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			log.Fatalf("Failed to create directory %s: %v", d, err)
		}
	}

	// 简单的 main.go 模版
	mainContent := `package main

import (
	"bear/pkg/bear"
	"context"
	"log/slog"
)

func main() {
	ctx := context.Background()
	app := bear.Ignite()
	if err := app.Launch(ctx); err != nil {
		slog.Error("Failed to launch", "error", err)
	}
}
`
	os.WriteFile(name+"/cmd/main.go", []byte(mainContent), 0644)
	fmt.Println("Project created successfully!")
}

func handleGen(args []string) {
	subCmd := args[0]
	switch subCmd {
	case "ioc":
		genCmd := flag.NewFlagSet("ioc", flag.ExitOnError)
		dir := genCmd.String("dir", ".", "directory to scan")
		out := genCmd.String("out", "bear_gen.go", "output file")
		pkg := genCmd.String("pkg", "main", "package name")
		genCmd.Parse(args[1:])

		scanner := gen.NewScanner(*dir)
		infos, err := scanner.Scan()
		if err != nil {
			log.Fatalf("Scan failed: %v", err)
		}

		generator := gen.NewGenerator(*pkg)
		if err := generator.Generate(infos, *out); err != nil {
			log.Fatalf("Generate failed: %v", err)
		}
		fmt.Printf("Generated %s successfully with %d struct(s)\n", *out, len(infos))

	case "controller":
		if len(args) < 2 {
			log.Fatal("Controller name is required")
		}
		name := strings.Title(args[1])
		generator := gen.NewGenerator("controllers")
		data := struct{ Name string }{Name: name}
		path := fmt.Sprintf("pkg/controllers/%s_controller.go", strings.ToLower(name))
		if err := generator.GenerateFromTemplate(gen.ControllerTemplate, data, path); err != nil {
			log.Fatalf("Failed to generate controller: %v", err)
		}
		fmt.Printf("Controller %s created at %s\n", name, path)

	case "service":
		if len(args) < 2 {
			log.Fatal("Service name is required")
		}
		name := strings.Title(args[1])
		generator := gen.NewGenerator("services")
		data := struct{ Name string }{Name: name}
		path := fmt.Sprintf("pkg/services/%s_service.go", strings.ToLower(name))
		if err := generator.GenerateFromTemplate(gen.ServiceTemplate, data, path); err != nil {
			log.Fatalf("Failed to generate service: %v", err)
		}
		fmt.Printf("Service %s created at %s\n", name, path)

	default:
		fmt.Printf("Unknown gen command: %s\n", subCmd)
	}
}

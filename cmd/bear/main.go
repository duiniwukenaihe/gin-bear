package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"unicode"

	"github.com/duiniwukenaihe/gin-bear/pkg/bear/gen"
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
		name + "/locales",
	}

	for _, d := range dirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			log.Fatalf("Failed to create directory %s: %v", d, err)
		}
	}

	// 简单的 main.go 模版
	mainContent := `package main

import (
	"context"
	"log/slog"

	"github.com/duiniwukenaihe/gin-bear/pkg/bear"
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
	os.WriteFile(name+"/go.mod", []byte(fmt.Sprintf(`module %s

go 1.25.0
`, name)), 0644)
	os.WriteFile(name+"/application.yaml", []byte(`server:
  port: 8080
  name: "gin-bear-app"

database:
  enabled: false

auth:
  jwt_secret: "replace-with-at-least-32-random-characters"
  token_expire_hours: 24
  public_paths:
    - "/health"
    - "/live"
    - "/ready"
    - "/version"
    - "/swagger/*"

websocket:
  check_origin: true

metrics:
  enabled: true
  path: "/metrics"
`), 0644)
	os.WriteFile(name+"/application-prod.yaml.example", []byte(`server:
  port: 8080
  name: "gin-bear-app"
  mode: "release"
  trusted_proxies:
    - "127.0.0.1"

database:
  enabled: true
  type: "postgres"
  host: "postgres"
  port: "5432"
  user: "gin_bear"
  password: "change-me"
  dbname: "gin_bear"
  sslmode: "disable"
  slow_query_threshold: "500ms"

auth:
  jwt_secret: "replace-with-at-least-32-random-characters"
  token_expire_hours: 24
  public_paths:
    - "/health"
    - "/live"
    - "/ready"
    - "/version"
    - "/metrics"
    - "/swagger/*"

websocket:
  check_origin: true
  allowed_origins:
    - "https://example.com"

metrics:
  enabled: true
  path: "/metrics"

plugins:
  enabled: false
  allowed_dirs:
    - "/app/plugins"
`), 0644)
	os.WriteFile(name+"/.env.example", []byte(`BEAR_ENV=prod
GIN_MODE=release
BEAR_SERVER_PORT=8080
JWT_SECRET=replace-with-at-least-32-random-characters
`), 0644)
	os.WriteFile(name+"/Dockerfile", []byte(generatedDockerfileContent()), 0644)
	fmt.Println("Project created successfully!")
}

func generatedDockerfileContent() string {
	versionPackage := "github.com/duiniwukenaihe/gin-bear/pkg/bear"
	return fmt.Sprintf(`FROM golang:1.25-alpine AS build
WORKDIR /src
RUN apk add --no-cache git ca-certificates
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_TIME=unknown
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w -X %[1]s.Version=${VERSION} -X %[1]s.Commit=${COMMIT} -X %[1]s.BuildTime=${BUILD_TIME}" -o /out/app ./cmd

FROM alpine:3.22
RUN addgroup -S app && adduser -S app -G app
WORKDIR /app
COPY --from=build /out/app /app/app
COPY application.yaml /app/application.yaml
USER app
EXPOSE 8080
ENTRYPOINT ["/app/app"]
`, versionPackage)
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
		name := exportedName(args[1])
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
		name := exportedName(args[1])
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

func exportedName(input string) string {
	parts := strings.FieldsFunc(input, func(r rune) bool {
		return r == '-' || r == '_' || unicode.IsSpace(r)
	})
	var b strings.Builder
	for _, part := range parts {
		b.WriteString(exportedPart(part))
	}
	return b.String()
}

func exportedPart(part string) string {
	if part == "" {
		return ""
	}
	if part == strings.ToUpper(part) {
		return part
	}
	runes := []rune(part)
	runes[0] = unicode.ToUpper(runes[0])
	return string(runes)
}

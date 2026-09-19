package scaffold

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/duiniwukenaihe/gin-bear/pkg/bear"
	"gopkg.in/yaml.v2"
)

func TestGeneratedProjectUsesStrictRuntimeContract(t *testing.T) {
	project := generateStrictRuntimeProject(t)

	type generatedConfig struct {
		Config struct {
			Strict                         bool  `yaml:"strict"`
			FrameworkStrict                bool  `yaml:"framework.strict"`
			AllowCompatibilityInProduction *bool `yaml:"framework.allow_compatibility_in_production"`
		} `yaml:"config"`
		Auth struct {
			Enabled     *bool    `yaml:"enabled"`
			PublicPaths []string `yaml:"public_paths"`
		} `yaml:"auth"`
		GRPC *struct {
			Enabled *bool `yaml:"enabled"`
		} `yaml:"grpc"`
	}

	readConfig := func(name string) (generatedConfig, string) {
		t.Helper()
		path := filepath.Join(project, name)
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var config generatedConfig
		if err := yaml.Unmarshal(contents, &config); err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		return config, string(contents)
	}

	development, _ := readConfig("application.yaml")
	production, productionSource := readConfig("application-prod.yaml.example")
	for name, config := range map[string]generatedConfig{
		"application.yaml":              development,
		"application-prod.yaml.example": production,
	} {
		if !config.Config.Strict || !config.Config.FrameworkStrict {
			t.Errorf("%s must enable strict config loading and strict framework runtime", name)
		}
		if config.Auth.Enabled == nil || *config.Auth.Enabled {
			t.Errorf("%s auth.enabled must be explicitly false", name)
		}
		if slices.Contains(config.Auth.PublicPaths, "/metrics") {
			t.Errorf("%s must not expose /metrics as an authentication public path", name)
		}
		if slices.Contains(config.Auth.PublicPaths, "/version") {
			t.Errorf("%s must not expose /version build metadata as an authentication public path", name)
		}
	}
	if development.GRPC == nil || development.GRPC.Enabled == nil || *development.GRPC.Enabled {
		t.Error("application.yaml grpc.enabled must be explicitly false")
	}
	if production.Config.AllowCompatibilityInProduction == nil || *production.Config.AllowCompatibilityInProduction {
		t.Error("application-prod.yaml.example must explicitly prohibit production compatibility mode")
	}
	if production.GRPC != nil {
		t.Error("application-prod.yaml.example gRPC guidance must remain fully commented out")
	}
	for _, want := range []string{
		"# grpc:",
		"#   enabled: false",
		"#   host: \"127.0.0.1\"",
		"#   transport_security: \"plaintext\"",
		"#   tls_cert_file: \"\"",
		"#   tls_key_file: \"\"",
		"#   client_ca_file: \"\"",
		"same-host Nginx or Envoy",
		"transport_security: \"tls\"",
		"transport_security: \"mtls\"",
	} {
		if !strings.Contains(productionSource, want) {
			t.Errorf("application-prod.yaml.example missing commented gRPC guidance %q", want)
		}
	}

	appSource, err := os.ReadFile(filepath.Join(project, "internal", "app", "app.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"bear.IgniteE(", ".EnableHealthE(", ".Serve(ctx)"} {
		if !strings.Contains(string(appSource), want) {
			t.Errorf("generated app.go missing error-returning startup call %q", want)
		}
	}
	for _, forbidden := range []string{"bear.Ignite(", ".EnableHealth()", ".Launch("} {
		if strings.Contains(string(appSource), forbidden) {
			t.Errorf("generated app.go uses compatibility startup call %q", forbidden)
		}
	}
}

func TestGeneratedStrictRuntimeTemplateConfiguration(t *testing.T) {
	project := generateStrictRuntimeProject(t)
	t.Setenv("BEAR_ENV", "dev")
	t.Setenv("GIN_MODE", "")
	t.Setenv("BEAR_AUTH_JWT_SECRET", "strict-runtime-template-test-secret-2026")
	t.Setenv("JWT_SECRET", "")

	paths := []string{
		filepath.Join(project, "application.yaml"),
		filepath.Join(project, "application-prod.yaml.example"),
		filepath.Join(repoRoot(t), "application-prod.yaml.example"),
	}
	for _, path := range paths {
		config, err := bear.LoadConfig(path)
		if err != nil {
			t.Fatalf("LoadConfig(%s): %v", path, err)
		}
		strict, ok := config.Config["strict"].(bool)
		if !ok || !strict {
			t.Errorf("%s config.strict = %#v, want true", path, config.Config["strict"])
		}
		if !config.FrameworkStrict() {
			t.Errorf("%s framework.strict = false, want true", path)
		}
		if mode := config.ResponseMode(); mode != "envelope" {
			t.Errorf("%s framework.response_mode = %q, want envelope", path, mode)
		}
	}
}

func TestGeneratedStrictRuntimeTemplateStartup(t *testing.T) {
	project := generateStrictRuntimeProject(t)
	appPath := filepath.Join(project, "internal", "app", "app.go")
	appSource, err := os.ReadFile(appPath)
	if err != nil {
		t.Fatal(err)
	}

	parsed, err := parser.ParseFile(token.NewFileSet(), appPath, appSource, 0)
	if err != nil {
		t.Fatalf("parse generated app.go: %v", err)
	}
	var calls []string
	ast.Inspect(parsed, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch function := call.Fun.(type) {
		case *ast.Ident:
			if function.Name == "configure" {
				calls = append(calls, function.Name)
			}
		case *ast.SelectorExpr:
			receiver, ok := function.X.(*ast.Ident)
			if !ok {
				return true
			}
			if receiver.Name == "bear" || receiver.Name == "application" {
				calls = append(calls, receiver.Name+"."+function.Sel.Name)
			}
			if receiver.Name == "application" && strings.HasPrefix(function.Sel.Name, "Enable") && !strings.HasSuffix(function.Sel.Name, "E") {
				t.Errorf("generated app.go uses non-error initialization API %s", function.Sel.Name)
			}
			if strings.Contains(function.Sel.Name, "CORS") || strings.Contains(function.Sel.Name, "Auth") || strings.Contains(function.Sel.Name, "JWT") {
				t.Errorf("generated app.go installs optional middleware %s", function.Sel.Name)
			}
		}
		return true
	})

	wantCalls := []string{
		"bear.IgniteE",
		"application.Shutdown",
		"application.EnableTracingE",
		"application.EnableDatabaseE",
		"application.EnableRedisE",
		"application.EnableMetricsE",
		"application.EnableHealthE",
		"application.AddModuleE",
		"configure",
		"application.Serve",
	}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("generated startup calls = %v, want %v\n%s", calls, wantCalls, appSource)
	}
	for _, stage := range []string{
		"initialize application",
		"initialize database",
		"initialize Redis",
		"initialize tracing",
		"initialize metrics",
		"initialize health",
		"register generated modules",
		"configure application",
		"serve application",
	} {
		if !strings.Contains(string(appSource), stage+`: %w`) {
			t.Errorf("generated app.go does not wrap the %q stage", stage)
		}
	}

	routesSource, err := os.ReadFile(filepath.Join(project, "internal", "app", "routes.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"func configure(application *bear.Bear) error",
		"return nil",
	} {
		if !strings.Contains(string(routesSource), want) {
			t.Errorf("generated routes.go missing %q:\n%s", want, routesSource)
		}
	}
}

func TestGeneratedWindowsShutdownSignalsCompileWithGoRuntime(t *testing.T) {
	project := generateStrictRuntimeProject(t)
	path := filepath.Join(project, "cmd", "server", "signals_windows.go")
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Inspect the referenced constants through the AST rather than by substring:
	// the file documents why syscall.SIGBREAK must not be used, and a text match
	// cannot tell that comment apart from a real reference.
	referenced := syscallConstantsIn(t, string(source))
	if slices.Contains(referenced, "SIGBREAK") {
		t.Fatalf("generated Windows signals reference unsupported syscall.SIGBREAK: %v", referenced)
	}
	// os.Interrupt covers Control-C and Control-Break on Windows, and
	// syscall.SIGTERM is what the runtime reports for CTRL_CLOSE_EVENT,
	// CTRL_LOGOFF_EVENT and CTRL_SHUTDOWN_EVENT, so a generated server still
	// reaches its graceful path when its console window is closed or the machine
	// shuts down.
	if !slices.Equal(referenced, []string{"SIGTERM"}) {
		t.Fatalf("generated Windows syscall constants = %v, want [SIGTERM]", referenced)
	}
	if !strings.Contains(string(source), "[]os.Signal{os.Interrupt, syscall.SIGTERM}") {
		t.Fatalf("generated Windows signals do not cover the graceful shutdown set:\n%s", source)
	}
	assertGeneratedFileCompilesFor(t, "windows", "amd64", string(source))
}

// syscallConstantsIn returns the sorted names of the syscall package constants
// the source refers to.
func syscallConstantsIn(t *testing.T, source string) []string {
	t.Helper()
	parsed, err := parser.ParseFile(token.NewFileSet(), "signals_windows.go", source, 0)
	if err != nil {
		t.Fatalf("parse generated Windows signals: %v", err)
	}
	var referenced []string
	ast.Inspect(parsed, func(node ast.Node) bool {
		selector, ok := node.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if ident, ok := selector.X.(*ast.Ident); ok && ident.Name == "syscall" {
			referenced = append(referenced, selector.Sel.Name)
		}
		return true
	})
	slices.Sort(referenced)
	return referenced
}

// assertGeneratedFileCompilesFor type-checks a rendered template file for the
// given target platform. A substring assertion cannot tell whether a referenced
// constant exists for that GOOS, which is how an unsupported syscall.SIGBREAK
// reference reached the Windows template in the first place. The rendered signal
// files import only the standard library, so checking them in isolation is far
// cheaper than cross-building the whole generated project.
func assertGeneratedFileCompilesFor(t *testing.T, goos, goarch, source string) {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"go.mod":     "module generatedplatformcheck\n\ngo 1.25\n",
		"main.go":    "package main\n\nfunc main() {}\n",
		"signals.go": source,
	}
	for name, contents := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0644); err != nil {
			t.Fatal(err)
		}
	}
	runGoForTarget(t, dir, goos, goarch, "build", "./...")
}

func TestProductionExamplesUseLocalServiceEndpoints(t *testing.T) {
	project := generateStrictRuntimeProject(t)
	paths := []string{
		filepath.Join(project, "application-prod.yaml.example"),
		filepath.Join(repoRoot(t), "application-prod.yaml.example"),
	}

	type productionConfig struct {
		Server struct {
			ReadHeaderTimeout   string `yaml:"read_header_timeout"`
			ReadTimeout         string `yaml:"read_timeout"`
			WriteTimeout        string `yaml:"write_timeout"`
			IdleTimeout         string `yaml:"idle_timeout"`
			ShutdownTimeout     string `yaml:"shutdown_timeout"`
			MaxHeaderBytes      int    `yaml:"max_header_bytes"`
			MaxRequestBodyBytes int64  `yaml:"max_request_body_bytes"`
		} `yaml:"server"`
		CORS *struct {
			Enabled bool `yaml:"enabled"`
		} `yaml:"cors"`
		Database struct {
			Host string `yaml:"host"`
		} `yaml:"database"`
		Redis struct {
			Addr string `yaml:"addr"`
		} `yaml:"redis"`
		Tracing struct {
			OTLPEndpoint string `yaml:"otlp_endpoint"`
		} `yaml:"tracing"`
	}

	for _, path := range paths {
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var config productionConfig
		if err := yaml.Unmarshal(contents, &config); err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		wantServer := struct {
			ReadHeaderTimeout   string `yaml:"read_header_timeout"`
			ReadTimeout         string `yaml:"read_timeout"`
			WriteTimeout        string `yaml:"write_timeout"`
			IdleTimeout         string `yaml:"idle_timeout"`
			ShutdownTimeout     string `yaml:"shutdown_timeout"`
			MaxHeaderBytes      int    `yaml:"max_header_bytes"`
			MaxRequestBodyBytes int64  `yaml:"max_request_body_bytes"`
		}{
			ReadHeaderTimeout:   "5s",
			ReadTimeout:         "15s",
			WriteTimeout:        "30s",
			IdleTimeout:         "60s",
			ShutdownTimeout:     "10s",
			MaxHeaderBytes:      1048576,
			MaxRequestBodyBytes: 1048576,
		}
		if config.Server != wantServer {
			t.Errorf("%s server production limits = %+v, want %+v", path, config.Server, wantServer)
		}
		if config.Database.Host != "127.0.0.1" {
			t.Errorf("%s database host = %q, want 127.0.0.1", path, config.Database.Host)
		}
		if config.Redis.Addr != "127.0.0.1:6379" {
			t.Errorf("%s redis address = %q, want 127.0.0.1:6379", path, config.Redis.Addr)
		}
		if config.Tracing.OTLPEndpoint != "http://127.0.0.1:4318/v1/traces" {
			t.Errorf("%s tracing endpoint = %q, want the local collector", path, config.Tracing.OTLPEndpoint)
		}
		if config.CORS == nil || config.CORS.Enabled {
			t.Errorf("%s must retain CORS as an opt-in setting", path)
		}
	}
}

func generateStrictRuntimeProject(t *testing.T) string {
	t.Helper()
	project := filepath.Join(t.TempDir(), "strict-runtime-api")
	if err := Generate(context.Background(), Options{
		Name:             "strict-runtime-api",
		Module:           "example.com/strict-runtime-api",
		Directory:        project,
		FrameworkVersion: "v0.9.2",
	}); err != nil {
		t.Fatal(err)
	}
	return project
}

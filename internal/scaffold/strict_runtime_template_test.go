package scaffold

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/duiniwukenaihe/gin-bear/pkg/bear"
	"gopkg.in/yaml.v2"
)

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
		"application.EnableDatabaseE",
		"application.EnableTracingE",
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

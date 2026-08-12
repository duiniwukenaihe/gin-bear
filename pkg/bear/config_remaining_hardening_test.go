package bear

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestProductionRequiresStrictRuntimeUnlessExplicitlyAllowed(t *testing.T) {
	t.Setenv("BEAR_ENV", "dev")
	t.Setenv("GIN_MODE", "")

	config := NewSysConfig()
	config.Server.Mode = gin.ReleaseMode
	config.Auth = nil
	if err := config.Validate(); err == nil || !strings.Contains(err.Error(), "framework.strict") {
		t.Fatalf("Validate() error = %v, want strict runtime rejection", err)
	}

	config.SetAllowCompatibilityInProduction(true)
	if err := config.Validate(); err != nil {
		t.Fatalf("explicit production compatibility should validate: %v", err)
	}
}

func TestDevelopmentAllowsCompatibilityRuntime(t *testing.T) {
	t.Setenv("BEAR_ENV", "dev")
	t.Setenv("GIN_MODE", "")

	config := NewSysConfig()
	config.Auth = nil
	if err := config.Validate(); err != nil {
		t.Fatalf("development compatibility should validate: %v", err)
	}
}

func TestAuthStorageTypeHasExplicitRuntimeContract(t *testing.T) {
	config := NewSysConfig()
	if config.Auth.StorageType != "jwt" {
		t.Fatalf("default auth storage type = %q, want jwt", config.Auth.StorageType)
	}

	config.Auth.StorageType = "database"
	if err := config.Validate(); err == nil || !strings.Contains(err.Error(), "auth.storage_type") {
		t.Fatalf("Validate() error = %v, want unsupported auth storage rejection", err)
	}

	config.Auth.StorageType = "file"
	if err := config.Validate(); err != nil {
		t.Fatalf("legacy file storage alias should remain compatible: %v", err)
	}
	warnings := config.compatibilityWarnings()
	if len(warnings) != 1 || !strings.Contains(warnings[0], "no file revocation store") {
		t.Fatalf("compatibility warnings = %#v, want file storage alias warning", warnings)
	}
}

func TestAllowCompatibilityInProductionMustBeBoolean(t *testing.T) {
	t.Setenv("BEAR_ENV", "dev")
	t.Setenv("GIN_MODE", "")

	config := NewSysConfig()
	config.SetFrameworkStrict(true)
	config.Config[frameworkAllowCompatibilityProductionKey] = "true"
	if err := config.Validate(); err == nil || !strings.Contains(err.Error(), frameworkAllowCompatibilityProductionKey) {
		t.Fatalf("Validate() error = %v, want boolean policy rejection", err)
	}
}

func TestProductionCompatibilityOptOutEmitsHighRiskWarning(t *testing.T) {
	t.Setenv("BEAR_ENV", "dev")
	t.Setenv("GIN_MODE", "")

	config := NewSysConfig()
	config.Server.Mode = gin.ReleaseMode
	config.SetAllowCompatibilityInProduction(true)
	warnings := config.compatibilityWarnings()
	if len(warnings) != 1 || !strings.Contains(warnings[0], "fail-soft IoC") {
		t.Fatalf("compatibility warnings = %#v, want production high-risk warning", warnings)
	}
}

func TestLoadConfigRejectsInvalidEnvironmentOverrides(t *testing.T) {
	tests := []struct {
		name     string
		variable string
		value    string
	}{
		{name: "server port format", variable: "BEAR_SERVER_PORT", value: "not-a-port"},
		{name: "server port empty", variable: "BEAR_SERVER_PORT", value: ""},
		{name: "server port below range", variable: "BEAR_SERVER_PORT", value: "0"},
		{name: "server port above range", variable: "BEAR_SERVER_PORT", value: "65536"},
		{name: "database max open format", variable: "DB_MAX_OPEN_CONNS", value: "many"},
		{name: "database max idle format", variable: "DB_MAX_IDLE_CONNS", value: "1.5"},
		{name: "redis required format", variable: "REDIS_REQUIRED", value: "sometimes"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearRemainingConfigEnvironment(t)
			t.Setenv("BEAR_ENV", "dev")
			t.Setenv(tt.variable, tt.value)
			path := writeConfig(t, "application.yaml", "server:\n  name: env-test\n")

			_, err := LoadConfig(path)
			if err == nil || !strings.Contains(err.Error(), tt.variable) {
				t.Fatalf("LoadConfig error = %v, want rejection containing %q", err, tt.variable)
			}
		})
	}
}

func TestApplyEnvOverridesRejectsInvalidValuesWithoutTargetSection(t *testing.T) {
	tests := []struct {
		name        string
		variable    string
		value       string
		clearTarget func(*SysConfig)
	}{
		{name: "server", variable: "BEAR_SERVER_PORT", value: "invalid", clearTarget: func(c *SysConfig) { c.Server = nil }},
		{name: "database max open", variable: "DB_MAX_OPEN_CONNS", value: "invalid", clearTarget: func(c *SysConfig) { c.DB = nil }},
		{name: "database max idle", variable: "DB_MAX_IDLE_CONNS", value: "invalid", clearTarget: func(c *SysConfig) { c.DB = nil }},
		{name: "redis", variable: "REDIS_REQUIRED", value: "invalid", clearTarget: func(c *SysConfig) { c.Redis = nil }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearRemainingConfigEnvironment(t)
			t.Setenv(tt.variable, tt.value)
			config := NewSysConfig()
			tt.clearTarget(config)

			err := applyEnvOverrides(config)
			if err == nil || !strings.Contains(err.Error(), tt.variable) {
				t.Fatalf("applyEnvOverrides error = %v, want rejection containing %q", err, tt.variable)
			}
		})
	}
}

func TestLoadConfigUsesNormalizedEnvironmentFilename(t *testing.T) {
	tests := []struct {
		name        string
		environment string
		filename    string
		serverName  string
	}{
		{name: "uppercase prod", environment: "PROD", filename: "application-prod.yaml", serverName: "prod-file"},
		{name: "mixed case production", environment: "Production", filename: "application-production.yaml", serverName: "production-file"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearRemainingConfigEnvironment(t)
			t.Setenv("BEAR_ENV", tt.environment)
			t.Setenv("JWT_SECRET", "remaining-hardening-random-jwt-key-2026")
			directory := t.TempDir()
			t.Chdir(directory)
			contents := []byte("server:\n  name: " + tt.serverName + "\nconfig:\n  framework.strict: true\n")
			if err := os.WriteFile(filepath.Join(directory, tt.filename), contents, 0o600); err != nil {
				t.Fatal(err)
			}

			config, err := LoadConfig()
			if err != nil {
				t.Fatalf("LoadConfig failed: %v", err)
			}
			if config.Server.Name != tt.serverName {
				t.Fatalf("server name = %q, want %q from %s", config.Server.Name, tt.serverName, tt.filename)
			}
		})
	}
}

func TestConfigFilenameEnvironmentUsesNormalizedName(t *testing.T) {
	tests := []struct {
		raw        string
		normalized string
	}{
		{raw: "PROD", normalized: "prod"},
		{raw: "Production", normalized: "production"},
	}

	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			if got := configFilenameEnvironment(tt.raw, tt.normalized); got != tt.normalized {
				t.Fatalf("config filename environment = %q, want %q", got, tt.normalized)
			}
		})
	}
}

func TestSysConfigValidateRejectsRemainingInvalidSemantics(t *testing.T) {
	clearRemainingConfigEnvironment(t)
	t.Setenv("BEAR_ENV", "dev")
	tests := []struct {
		name    string
		mutate  func(*SysConfig)
		wantErr string
	}{
		{name: "server port below range", mutate: func(c *SysConfig) { c.Server.Port = -1 }, wantErr: "server.port"},
		{name: "server port above range", mutate: func(c *SysConfig) { c.Server.Port = 65536 }, wantErr: "server.port"},
		{name: "grpc port below range", mutate: func(c *SysConfig) { c.GRPC.Enabled = true; c.GRPC.Port = 0 }, wantErr: "grpc.port"},
		{name: "grpc port above range", mutate: func(c *SysConfig) { c.GRPC.Enabled = true; c.GRPC.Port = 65536 }, wantErr: "grpc.port"},
		{name: "server and grpc port conflict", mutate: func(c *SysConfig) { c.GRPC.Enabled = true; c.GRPC.Port = c.Server.Port }, wantErr: "must differ"},
		{name: "database max open negative", mutate: func(c *SysConfig) { c.DB.MaxOpenConns = -1 }, wantErr: "database.max_open_conns"},
		{name: "database max idle negative", mutate: func(c *SysConfig) { c.DB.MaxIdleConns = -1 }, wantErr: "database.max_idle_conns"},
		{name: "database idle exceeds open", mutate: func(c *SysConfig) { c.DB.MaxOpenConns = 2; c.DB.MaxIdleConns = 3 }, wantErr: "database.max_idle_conns"},
		{name: "redis database negative", mutate: func(c *SysConfig) { c.Redis.DB = -1 }, wantErr: "redis.db"},
		{name: "redis pool negative", mutate: func(c *SysConfig) { c.Redis.PoolSize = -1 }, wantErr: "redis.pool_size"},
		{name: "redis idle negative", mutate: func(c *SysConfig) { c.Redis.MinIdleConns = -1 }, wantErr: "redis.min_idle_conns"},
		{name: "redis dial timeout negative", mutate: func(c *SysConfig) { c.Redis.DialTimeout = -1 }, wantErr: "redis.dial_timeout_seconds"},
		{name: "redis read timeout negative", mutate: func(c *SysConfig) { c.Redis.ReadTimeout = -1 }, wantErr: "redis.read_timeout_seconds"},
		{name: "redis write timeout negative", mutate: func(c *SysConfig) { c.Redis.WriteTimeout = -1 }, wantErr: "redis.write_timeout_seconds"},
		{name: "redis idle exceeds pool", mutate: func(c *SysConfig) { c.Redis.PoolSize = 2; c.Redis.MinIdleConns = 3 }, wantErr: "redis.min_idle_conns"},
		{name: "cors max age invalid", mutate: func(c *SysConfig) { c.CORS.MaxAge = "later" }, wantErr: "cors.max_age"},
		{name: "cors max age negative", mutate: func(c *SysConfig) { c.CORS.MaxAge = "-1s" }, wantErr: "cors.max_age"},
		{name: "tracing endpoint relative", mutate: func(c *SysConfig) { c.Tracing.OTLPEndpoint = "localhost:4318" }, wantErr: "tracing.otlp_endpoint"},
		{name: "tracing endpoint unsupported scheme", mutate: func(c *SysConfig) { c.Tracing.OTLPEndpoint = "grpc://localhost:4317" }, wantErr: "tracing.otlp_endpoint"},
		{name: "tracing endpoint missing host", mutate: func(c *SysConfig) { c.Tracing.OTLPEndpoint = "https:collector" }, wantErr: "tracing.otlp_endpoint"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := NewSysConfig()
			tt.mutate(config)

			err := config.Validate()
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), tt.wantErr) {
				t.Fatalf("Validate error = %v, want rejection containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestSysConfigValidateAcceptsRemainingSemanticBoundaries(t *testing.T) {
	clearRemainingConfigEnvironment(t)
	t.Setenv("BEAR_ENV", "dev")
	tests := []struct {
		name   string
		mutate func(*SysConfig)
	}{
		{name: "minimum server port", mutate: func(c *SysConfig) { c.Server.Port = 1 }},
		{name: "maximum server port", mutate: func(c *SysConfig) { c.Server.Port = 65535 }},
		{name: "minimum grpc port", mutate: func(c *SysConfig) { c.GRPC.Enabled = true; c.GRPC.Port = 1 }},
		{name: "maximum grpc port", mutate: func(c *SysConfig) { c.GRPC.Enabled = true; c.GRPC.Port = 65535 }},
		{name: "database idle with default open", mutate: func(c *SysConfig) { c.DB.MaxOpenConns = 0; c.DB.MaxIdleConns = 3 }},
		{name: "database equal positive pools", mutate: func(c *SysConfig) { c.DB.MaxOpenConns = 3; c.DB.MaxIdleConns = 3 }},
		{name: "redis zero values", mutate: func(c *SysConfig) {}},
		{name: "redis equal positive pools", mutate: func(c *SysConfig) { c.Redis.PoolSize = 3; c.Redis.MinIdleConns = 3 }},
		{name: "cors zero max age", mutate: func(c *SysConfig) { c.CORS.MaxAge = "0s" }},
		{name: "http tracing endpoint", mutate: func(c *SysConfig) { c.Tracing.OTLPEndpoint = "http://localhost:4318" }},
		{name: "https tracing endpoint", mutate: func(c *SysConfig) { c.Tracing.OTLPEndpoint = "https://collector.example/v1/traces" }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := NewSysConfig()
			tt.mutate(config)

			if err := config.Validate(); err != nil {
				t.Fatalf("Validate rejected boundary configuration: %v", err)
			}
		})
	}
}

func TestSysConfigValidateDoesNotEnableCORS(t *testing.T) {
	clearRemainingConfigEnvironment(t)
	t.Setenv("BEAR_ENV", "dev")
	config := NewSysConfig()
	config.CORS.Enabled = false
	config.CORS.MaxAge = "1h"

	if err := config.Validate(); err != nil {
		t.Fatalf("Validate failed: %v", err)
	}
	if config.CORS.Enabled {
		t.Fatal("Validate enabled CORS")
	}
}

func clearRemainingConfigEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"BEAR_ENV",
		"GIN_MODE",
		"BEAR_SERVER_PORT",
		"DB_MAX_OPEN_CONNS",
		"DB_MAX_IDLE_CONNS",
		"REDIS_REQUIRED",
		"BEAR_AUTH_JWT_SECRET",
		"JWT_SECRET",
	} {
		value, existed := os.LookupEnv(name)
		if err := os.Unsetenv(name); err != nil {
			t.Fatalf("unset %s: %v", name, err)
		}
		t.Cleanup(func() {
			if existed {
				_ = os.Setenv(name, value)
			} else {
				_ = os.Unsetenv(name)
			}
		})
	}
}

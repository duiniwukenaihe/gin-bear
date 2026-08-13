package bear

import (
	"context"
	"crypto/tls"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

func TestLoadConfigRecognizesRedisTLSSettings(t *testing.T) {
	t.Setenv("BEAR_ENV", "dev")
	path := writeConfig(t, "application.yaml", `
server:
  name: redis-tls-loader
redis:
  tls:
    enabled: true
    server_name: redis.internal.example
    ca_file: /run/secrets/redis-ca.pem
    cert_file: /run/secrets/redis-client.pem
    key_file: /run/secrets/redis-client.key
`)

	config, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() failed: %v", err)
	}
	if config.Redis == nil || !config.Redis.TLS.Enabled {
		t.Fatalf("Redis TLS config = %#v, want enabled", config.Redis)
	}
	if config.Redis.TLS.ServerName != "redis.internal.example" {
		t.Fatalf("Redis TLS server name = %q", config.Redis.TLS.ServerName)
	}
	if config.Redis.TLS.CAFile != "/run/secrets/redis-ca.pem" {
		t.Fatalf("Redis TLS CA file = %q", config.Redis.TLS.CAFile)
	}
	if config.Redis.TLS.CertFile != "/run/secrets/redis-client.pem" || config.Redis.TLS.KeyFile != "/run/secrets/redis-client.key" {
		t.Fatalf("Redis TLS client certificate settings = %#v", config.Redis.TLS)
	}
}

func TestRedisOptionsBuildsTLSConfigWithCAAndClientCertificate(t *testing.T) {
	files := grpcSecurityGenerateCertificates(t)

	options, err := redisOptions(&RedisConfig{
		Addr: "redis.internal.example:6379",
		TLS: RedisTLSConfig{
			Enabled:    true,
			ServerName: "redis.internal.example",
			CAFile:     files.caCert,
			CertFile:   files.serverCert,
			KeyFile:    files.serverKey,
		},
	})
	if err != nil {
		t.Fatalf("redisOptions() failed: %v", err)
	}
	if options.TLSConfig == nil {
		t.Fatal("redisOptions() returned no TLS config")
	}
	if options.TLSConfig.MinVersion != tls.VersionTLS12 {
		t.Fatalf("TLS minimum version = %d, want TLS 1.2", options.TLSConfig.MinVersion)
	}
	if options.TLSConfig.ServerName != "redis.internal.example" {
		t.Fatalf("TLS server name = %q", options.TLSConfig.ServerName)
	}
	if options.TLSConfig.RootCAs == nil {
		t.Fatal("TLS root CA pool is nil")
	}
	if len(options.TLSConfig.Certificates) != 1 {
		t.Fatalf("TLS client certificates = %d, want 1", len(options.TLSConfig.Certificates))
	}
}

func TestOpenRedisAdapterContextReturnsTLSConfigurationErrors(t *testing.T) {
	adapter, err := OpenRedisAdapterContext(context.Background(), &RedisConfig{
		Addr: "redis.internal.example:6379",
		TLS: RedisTLSConfig{
			Enabled: true,
			CAFile:  "/missing/redis-ca.pem",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "read Redis TLS CA") {
		t.Fatalf("OpenRedisAdapterContext() error = %v, want Redis TLS CA error", err)
	}
	if adapter != nil {
		t.Fatalf("OpenRedisAdapterContext() adapter = %#v, want nil", adapter)
	}
}

func TestOpenRedisAdapterContextRejectsProductionRemotePlaintext(t *testing.T) {
	t.Setenv("BEAR_ENV", "prod")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	adapter, err := OpenRedisAdapterContext(ctx, &RedisConfig{
		Addr: "203.0.113.1:6379",
	})
	if err == nil || !strings.Contains(err.Error(), "production remote Redis requires TLS") {
		t.Fatalf("OpenRedisAdapterContext() error = %v, want production remote Redis TLS rejection", err)
	}
	if adapter != nil {
		t.Fatalf("OpenRedisAdapterContext() adapter = %#v, want nil", adapter)
	}
}

func TestOpenRedisAdapterContextRejectsReleaseModeRemotePlaintext(t *testing.T) {
	resetGinModeForTest(t)
	t.Setenv("BEAR_ENV", "dev")
	config := NewSysConfig()
	config.Server.Mode = gin.ReleaseMode
	config.SetFrameworkStrict(true)
	config.Redis.Addr = "203.0.113.1:6379"
	app, err := IgniteE(config)
	if err != nil {
		t.Fatalf("IgniteE() error = %v", err)
	}
	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })

	adapter, err := OpenRedisAdapterContext(context.Background(), &RedisConfig{Addr: "203.0.113.1:6379"})
	if err == nil || !strings.Contains(err.Error(), "production remote Redis requires TLS") {
		t.Fatalf("OpenRedisAdapterContext() error = %v, want release-mode remote Redis TLS rejection", err)
	}
	if adapter != nil {
		t.Fatalf("OpenRedisAdapterContext() adapter = %#v, want nil", adapter)
	}
}

func TestOpenRedisAdapterContextUsesExplicitRuntimeTracingPolicy(t *testing.T) {
	t.Setenv("BEAR_ENV", "dev")
	server := miniredis.RunT(t)
	tracingCalls := 0
	provider := noop.NewTracerProvider()
	var instrumentedProvider trace.TracerProvider
	instrument := func(_ *redis.Client, received trace.TracerProvider) error {
		tracingCalls++
		instrumentedProvider = received
		return nil
	}

	adapter, err := openRedisAdapterContext(context.Background(), &RedisConfig{Addr: server.Addr()}, redisRuntimeOptions{
		tracing:           false,
		instrumentTracing: instrument,
	})
	if err != nil {
		t.Fatalf("openRedisAdapterContext(tracing=false) error = %v", err)
	}
	if err := adapter.Shutdown(); err != nil {
		t.Fatalf("RedisAdapter.Shutdown() error = %v", err)
	}
	if tracingCalls != 0 {
		t.Fatalf("tracing calls = %d, want 0", tracingCalls)
	}

	adapter, err = openRedisAdapterContext(context.Background(), &RedisConfig{Addr: server.Addr()}, redisRuntimeOptions{
		tracing:           true,
		tracerProvider:    provider,
		instrumentTracing: instrument,
	})
	if err != nil {
		t.Fatalf("openRedisAdapterContext(tracing=true) error = %v", err)
	}
	if err := adapter.Shutdown(); err != nil {
		t.Fatalf("RedisAdapter.Shutdown() error = %v", err)
	}
	if tracingCalls != 1 {
		t.Fatalf("tracing calls = %d, want 1", tracingCalls)
	}
	if instrumentedProvider != provider {
		t.Fatalf("instrumented provider = %T, want runtime provider %T", instrumentedProvider, provider)
	}
}

func TestRedisRuntimeOptionsAreOwnedByTheirRuntime(t *testing.T) {
	tracedConfig := NewSysConfig()
	tracedConfig.Tracing.Enabled = true
	tracedRuntime := newRuntime(tracedConfig)
	tracedRuntime.TracerProvider = noop.NewTracerProvider()
	plainConfig := NewSysConfig()
	plainConfig.Tracing.Enabled = false
	plainConfig.Server.Mode = gin.ReleaseMode

	traced := redisRuntimeOptionsFromRuntime(tracedRuntime)
	plain := redisRuntimeOptionsFromRuntime(newRuntime(plainConfig))
	if !traced.tracing || traced.production {
		t.Fatalf("traced runtime options = %#v, want tracing dev", traced)
	}
	if traced.tracerProvider != tracedRuntime.TracerProvider {
		t.Fatal("traced runtime options did not retain the owning TracerProvider")
	}
	if plain.tracing || !plain.production {
		t.Fatalf("plain runtime options = %#v, want non-tracing production", plain)
	}
}

func TestOpenRedisAdapterContextAlwaysRejectsRemotePlaintextWithoutRuntime(t *testing.T) {
	resetGinModeForTest(t)
	t.Setenv("BEAR_ENV", "dev")
	gin.SetMode(gin.DebugMode)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()

	adapter, err := OpenRedisAdapterContext(ctx, &RedisConfig{Addr: "203.0.113.1:6379"})
	if err == nil || !strings.Contains(err.Error(), "remote Redis requires TLS") {
		t.Fatalf("OpenRedisAdapterContext() error = %v, want remote plaintext rejection", err)
	}
	if adapter != nil {
		t.Fatalf("OpenRedisAdapterContext() adapter = %#v, want nil", adapter)
	}
}

func TestRedisTracingRequiresOwningTracerProvider(t *testing.T) {
	server := miniredis.RunT(t)
	adapter, err := openRedisAdapterContext(context.Background(), &RedisConfig{Addr: server.Addr()}, redisRuntimeOptions{tracing: true})
	if err == nil || !strings.Contains(err.Error(), "TracerProvider") {
		t.Fatalf("openRedisAdapterContext() error = %v, want TracerProvider requirement", err)
	}
	if adapter != nil {
		t.Fatalf("openRedisAdapterContext() adapter = %#v, want nil", adapter)
	}
}

func TestRedisOptionsRejectsProductionRemotePlaintext(t *testing.T) {
	t.Setenv("BEAR_ENV", "production")

	options, err := redisOptions(&RedisConfig{Addr: "redis.example.internal:6379"})
	if err == nil || !strings.Contains(err.Error(), "production remote Redis requires TLS") {
		t.Fatalf("redisOptions() error = %v, want production remote Redis TLS rejection", err)
	}
	if options != nil {
		t.Fatalf("redisOptions() options = %#v, want nil", options)
	}
}

func TestOpenRedisAdapterContextAllowsProductionLoopbackPlaintext(t *testing.T) {
	t.Setenv("BEAR_ENV", "prod")
	server := miniredis.RunT(t)

	adapter, err := OpenRedisAdapterContext(context.Background(), &RedisConfig{Addr: server.Addr()})
	if err != nil {
		t.Fatalf("OpenRedisAdapterContext() loopback plaintext error = %v", err)
	}
	if err := adapter.Shutdown(); err != nil {
		t.Fatalf("RedisAdapter.Shutdown() error = %v", err)
	}
}

func TestOpenRedisAdapterContextAllowsProductionRemoteTLS(t *testing.T) {
	t.Setenv("BEAR_ENV", "prod")
	files := grpcSecurityGenerateCertificates(t)
	certificate, err := tls.LoadX509KeyPair(files.serverCert, files.serverKey)
	if err != nil {
		t.Fatalf("tls.LoadX509KeyPair() failed: %v", err)
	}
	server, err := miniredis.RunTLS(&tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{certificate},
	})
	if err != nil {
		t.Fatalf("miniredis.RunTLS() failed: %v", err)
	}
	t.Cleanup(server.Close)
	_, port, err := net.SplitHostPort(server.Addr())
	if err != nil {
		t.Fatalf("net.SplitHostPort(%q) failed: %v", server.Addr(), err)
	}

	adapter, err := OpenRedisAdapterContext(context.Background(), &RedisConfig{
		Addr: net.JoinHostPort("localhost", port),
		TLS: RedisTLSConfig{
			Enabled:    true,
			ServerName: "localhost",
			CAFile:     files.caCert,
		},
	})
	if err != nil {
		t.Fatalf("OpenRedisAdapterContext() remote TLS error = %v", err)
	}
	if err := adapter.Shutdown(); err != nil {
		t.Fatalf("RedisAdapter.Shutdown() error = %v", err)
	}
}

func TestRedisOptionsRejectsUnpairedClientCertificate(t *testing.T) {
	_, err := redisOptions(&RedisConfig{
		TLS: RedisTLSConfig{
			Enabled:  true,
			CertFile: "/run/secrets/redis-client.pem",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "redis.tls.cert_file and redis.tls.key_file") {
		t.Fatalf("redisOptions() error = %v, want client certificate pair error", err)
	}
}

func TestNewRedisAdapterDoesNotFallBackToPlaintextForInvalidTLS(t *testing.T) {
	adapter := NewRedisAdapter(&RedisConfig{
		Addr: "redis.internal.example:6379",
		TLS: RedisTLSConfig{
			Enabled: true,
			CAFile:  "/missing/redis-ca.pem",
		},
	})
	if adapter == nil {
		t.Fatal("NewRedisAdapter() returned nil adapter")
	}
	if adapter.Client != nil {
		_ = adapter.Shutdown()
		t.Fatal("NewRedisAdapter() created a plaintext client after TLS configuration failed")
	}
}

func TestNewRedisAdapterDoesNotBypassProductionRemoteTLSRequirement(t *testing.T) {
	t.Setenv("BEAR_ENV", "prod")

	adapter := NewRedisAdapter(&RedisConfig{Addr: "203.0.113.1:6379"})
	if adapter == nil {
		t.Fatal("NewRedisAdapter() returned nil adapter")
	}
	if adapter.Client != nil {
		_ = adapter.Shutdown()
		t.Fatal("NewRedisAdapter() created a plaintext client for production remote Redis")
	}
}

func TestProductionRequiredRemoteRedisRejectsPlaintext(t *testing.T) {
	config := NewSysConfig()
	config.Server.Mode = gin.ReleaseMode
	config.SetFrameworkStrict(true)
	config.Redis.Required = true
	config.Redis.Addr = "redis.example.internal:6379"

	err := config.Validate()
	if err == nil || !strings.Contains(err.Error(), "remote Redis requires TLS") {
		t.Fatalf("Validate() error = %v, want remote Redis TLS rejection", err)
	}

	config.Redis.Addr = "127.0.0.1:6379"
	if err := config.Validate(); err != nil {
		t.Fatalf("Validate() loopback plaintext error = %v", err)
	}

	config.Redis.Addr = "redis.example.internal:6379"
	config.Redis.TLS.Enabled = true
	if err := config.Validate(); err != nil {
		t.Fatalf("Validate() remote TLS error = %v", err)
	}
}

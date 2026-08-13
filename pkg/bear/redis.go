package bear

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/redis/go-redis/extra/redisotel/v9"
	"github.com/redis/go-redis/v9"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// RedisConfig Redis 配置
type RedisConfig struct {
	Addr         string         `yaml:"addr" json:"addr"`
	Password     string         `yaml:"password" json:"password"`
	DB           int            `yaml:"db" json:"db"`
	PoolSize     int            `yaml:"pool_size" json:"pool_size"`
	MinIdleConns int            `yaml:"min_idle_conns" json:"min_idle_conns"`
	DialTimeout  int            `yaml:"dial_timeout_seconds" json:"dial_timeout_seconds"`
	ReadTimeout  int            `yaml:"read_timeout_seconds" json:"read_timeout_seconds"`
	WriteTimeout int            `yaml:"write_timeout_seconds" json:"write_timeout_seconds"`
	Required     bool           `yaml:"required" json:"required"`
	TLS          RedisTLSConfig `yaml:"tls" json:"tls"`
}

// RedisTLSConfig configures TLS for Redis connections.
type RedisTLSConfig struct {
	Enabled    bool   `yaml:"enabled" json:"enabled"`
	ServerName string `yaml:"server_name" json:"server_name"`
	CAFile     string `yaml:"ca_file" json:"ca_file"`
	CertFile   string `yaml:"cert_file" json:"cert_file"`
	KeyFile    string `yaml:"key_file" json:"key_file"`
}

// RedisAdapter Redis 适配器
type RedisAdapter struct {
	Client *redis.Client
}

func (r *RedisAdapter) Name() string {
	return "RedisAdapter"
}

func (r *RedisAdapter) Shutdown() error {
	if r == nil || r.Client == nil {
		return nil
	}
	slog.Info("Closing Redis connection pool...")
	return r.Client.Close()
}

func (r *RedisAdapter) CheckReady(ctx context.Context) error {
	if r == nil || r.Client == nil {
		return errors.New("redis client is unavailable")
	}
	return r.Client.Ping(ctx).Err()
}

func (*RedisAdapter) lifecyclePrestarted() bool { return true }

// OpenRedisAdapter creates a Redis adapter and reports startup ping failures.
func OpenRedisAdapter(cfg *RedisConfig) (*RedisAdapter, error) {
	return OpenRedisAdapterContext(context.Background(), cfg)
}

// OpenRedisAdapterContext creates and verifies a Redis adapter within ctx.
func OpenRedisAdapterContext(ctx context.Context, cfg *RedisConfig) (*RedisAdapter, error) {
	return openRedisAdapterContext(ctx, cfg, redisRuntimeOptions{
		production: true,
	})
}

type redisRuntimeOptions struct {
	production        bool
	tracing           bool
	tracerProvider    oteltrace.TracerProvider
	instrumentTracing func(*redis.Client, oteltrace.TracerProvider) error
}

func redisRuntimeOptionsFromRuntime(runtime *Runtime) redisRuntimeOptions {
	if runtime == nil || runtime.Config == nil {
		return redisRuntimeOptions{}
	}
	config := runtime.Config
	return redisRuntimeOptions{
		production:     isProductionMode(config),
		tracing:        config.Tracing != nil && config.Tracing.Enabled,
		tracerProvider: runtime.TracerProvider,
	}
}

func openRedisAdapterContext(ctx context.Context, cfg *RedisConfig, runtimeOptions redisRuntimeOptions) (*RedisAdapter, error) {
	if cfg == nil {
		return nil, errors.New("redis config is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("redis startup canceled: %w", err)
	}
	options, err := redisOptionsForRuntime(cfg, runtimeOptions.production)
	if err != nil {
		return nil, err
	}
	if runtimeOptions.tracing && runtimeOptions.tracerProvider == nil {
		return nil, errors.New("redis tracing requires the owning Runtime TracerProvider; call EnableTracingE before EnableRedisE")
	}
	client := redis.NewClient(options)
	adapter := &RedisAdapter{Client: client}

	if runtimeOptions.tracing {
		instrumentTracing := runtimeOptions.instrumentTracing
		if instrumentTracing == nil {
			instrumentTracing = func(client *redis.Client, provider oteltrace.TracerProvider) error {
				return redisotel.InstrumentTracing(client, redisotel.WithTracerProvider(provider))
			}
		}
		if err := instrumentTracing(client, runtimeOptions.tracerProvider); err != nil {
			slog.Error("Failed to instrument Redis with OTEL", "error", err)
		}
	}

	// 测试连接
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if err := client.Ping(pingCtx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("ping Redis at %s: %w", cfg.Addr, err)
	}

	slog.Info("Redis connected successfully", "addr", cfg.Addr)
	return adapter, nil
}

// NewRedisAdapter creates a Redis adapter using the legacy panic/fail-open policy.
// Deprecated: use OpenRedisAdapter to handle startup errors explicitly.
func NewRedisAdapter(cfg *RedisConfig) *RedisAdapter {
	adapter, err := OpenRedisAdapter(cfg)
	if err == nil {
		return adapter
	}
	addr := ""
	required := false
	if cfg != nil {
		addr = cfg.Addr
		required = cfg.Required
	}
	slog.Error("Failed to connect to Redis", "error", err, "addr", addr)
	if required {
		panic("required redis connection failed: " + err.Error())
	}
	if adapter == nil && cfg != nil {
		options, optionsErr := redisOptions(cfg)
		if optionsErr == nil {
			adapter = &RedisAdapter{Client: redis.NewClient(options)}
		} else {
			adapter = &RedisAdapter{}
		}
	}
	return adapter
}

func redisOptions(cfg *RedisConfig) (*redis.Options, error) {
	return redisOptionsForRuntime(cfg, true)
}

func redisOptionsForRuntime(cfg *RedisConfig, production bool) (*redis.Options, error) {
	if cfg == nil {
		return nil, errors.New("redis config is required")
	}
	if production && !cfg.TLS.Enabled && !redisAddressIsLoopback(cfg.Addr) {
		return nil, errors.New("production remote Redis requires TLS or a loopback proxy address")
	}

	opts := &redis.Options{
		Addr:     cfg.Addr,
		Password: cfg.Password,
		DB:       cfg.DB,
	}

	// 动态连接池与超时配置
	if cfg.PoolSize > 0 {
		opts.PoolSize = cfg.PoolSize
	}
	if cfg.MinIdleConns > 0 {
		opts.MinIdleConns = cfg.MinIdleConns
	}
	if cfg.DialTimeout > 0 {
		opts.DialTimeout = time.Duration(cfg.DialTimeout) * time.Second
	}
	if cfg.ReadTimeout > 0 {
		opts.ReadTimeout = time.Duration(cfg.ReadTimeout) * time.Second
	}
	if cfg.WriteTimeout > 0 {
		opts.WriteTimeout = time.Duration(cfg.WriteTimeout) * time.Second
	}
	tlsConfig, err := redisTLSConfig(cfg.TLS)
	if err != nil {
		return nil, err
	}
	opts.TLSConfig = tlsConfig
	return opts, nil
}

func redisTLSConfig(cfg RedisTLSConfig) (*tls.Config, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	certFile := strings.TrimSpace(cfg.CertFile)
	keyFile := strings.TrimSpace(cfg.KeyFile)
	if (certFile == "") != (keyFile == "") {
		return nil, errors.New("redis.tls.cert_file and redis.tls.key_file must be configured together")
	}

	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
		ServerName: strings.TrimSpace(cfg.ServerName),
	}
	if caFile := strings.TrimSpace(cfg.CAFile); caFile != "" {
		caPEM, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("read Redis TLS CA: %w", err)
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(caPEM) {
			return nil, errors.New("parse Redis TLS CA: no certificates found")
		}
		tlsConfig.RootCAs = roots
	}
	if certFile != "" {
		certificate, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, fmt.Errorf("load Redis TLS client certificate: %w", err)
		}
		tlsConfig.Certificates = []tls.Certificate{certificate}
	}
	return tlsConfig, nil
}

// CacheUtil 缓存操作工具类
type CacheUtil struct {
	Adapter *RedisAdapter `inject:"-"`
}

func (r *CacheUtil) Name() string {
	return "CacheUtil"
}

// Set 序列化并存储对象
func (r *CacheUtil) Set(ctx context.Context, key string, value interface{}, expiration time.Duration) error {
	b, err := sonic.Marshal(value)
	if err != nil {
		return err
	}
	return r.Adapter.Client.Set(ctx, key, b, expiration).Err()
}

// Get 获取并反序列化对象
func (r *CacheUtil) Get(ctx context.Context, key string, dest interface{}) error {
	val, err := r.Adapter.Client.Get(ctx, key).Result()
	if err != nil {
		return err
	}
	return sonic.Unmarshal([]byte(val), dest)
}

// Del 删除键
func (r *CacheUtil) Del(ctx context.Context, keys ...string) error {
	return r.Adapter.Client.Del(ctx, keys...).Err()
}

package bear

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/bytedance/sonic"
	"github.com/redis/go-redis/extra/redisotel/v9"
	"github.com/redis/go-redis/v9"
)

// RedisConfig Redis 配置
type RedisConfig struct {
	Addr         string `yaml:"addr"`
	Password     string `yaml:"password"`
	DB           int    `yaml:"db"`
	PoolSize     int    `yaml:"pool_size"`
	MinIdleConns int    `yaml:"min_idle_conns"`
	DialTimeout  int    `yaml:"dial_timeout_seconds"`
	ReadTimeout  int    `yaml:"read_timeout_seconds"`
	WriteTimeout int    `yaml:"write_timeout_seconds"`
	Required     bool   `yaml:"required"`
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
	if cfg == nil {
		return nil, errors.New("redis config is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("redis startup canceled: %w", err)
	}
	client := redis.NewClient(redisOptions(cfg))
	adapter := &RedisAdapter{Client: client}

	// 链路追踪集成
	config := GetByType[*SysConfig]()
	if config != nil && config.Tracing != nil && config.Tracing.Enabled {
		if err := redisotel.InstrumentTracing(client); err != nil {
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
		adapter = &RedisAdapter{Client: redis.NewClient(redisOptions(cfg))}
	}
	return adapter
}

func redisOptions(cfg *RedisConfig) *redis.Options {
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
	return opts
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

package bear

import (
	"context"
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
}

// RedisAdapter Redis 适配器
type RedisAdapter struct {
	Client *redis.Client
}

func (this *RedisAdapter) Name() string {
	return "RedisAdapter"
}

func (this *RedisAdapter) Shutdown() error {
	slog.Info("Closing Redis connection pool...")
	return this.Client.Close()
}

func (this *RedisAdapter) CheckReady(ctx context.Context) error {
	return this.Client.Ping(ctx).Err()
}

// NewRedisAdapter 创建 Redis 适配器
func NewRedisAdapter(cfg *RedisConfig) *RedisAdapter {
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

	client := redis.NewClient(opts)

	// 链路追踪集成
	config := GetByType[*SysConfig]()
	if config != nil && config.Tracing != nil && config.Tracing.Enabled {
		if err := redisotel.InstrumentTracing(client); err != nil {
			slog.Error("Failed to instrument Redis with OTEL", "error", err)
		}
	}

	// 测试连接
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		slog.Error("Failed to connect to Redis", "error", err, "addr", cfg.Addr)
		// 生产环境中，Redis 失败可能不一定 panic，取决于业务是否强依赖缓存
		return &RedisAdapter{Client: client}
	}

	slog.Info("Redis connected successfully", "addr", cfg.Addr)
	return &RedisAdapter{Client: client}
}

// CacheUtil 缓存操作工具类
type CacheUtil struct {
	Adapter *RedisAdapter `inject:"-"`
}

func (this *CacheUtil) Name() string {
	return "CacheUtil"
}

// Set 序列化并存储对象
func (this *CacheUtil) Set(ctx context.Context, key string, value interface{}, expiration time.Duration) error {
	b, err := sonic.Marshal(value)
	if err != nil {
		return err
	}
	return this.Adapter.Client.Set(ctx, key, b, expiration).Err()
}

// Get 获取并反序列化对象
func (this *CacheUtil) Get(ctx context.Context, key string, dest interface{}) error {
	val, err := this.Adapter.Client.Get(ctx, key).Result()
	if err != nil {
		return err
	}
	return sonic.Unmarshal([]byte(val), dest)
}

// Del 删除键
func (this *CacheUtil) Del(ctx context.Context, keys ...string) error {
	return this.Adapter.Client.Del(ctx, keys...).Err()
}

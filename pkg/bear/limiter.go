package bear

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// RateLimiter 限流器接口
type RateLimiter interface {
	Allow(ctx context.Context, key string) bool
}

// MemoryRateLimiter 基于内存的简单计数限流器 (演示用)
type MemoryRateLimiter struct {
	mu       sync.RWMutex
	counts   map[string]int
	limit    int
	window   time.Duration
	done     chan struct{}
	stopOnce sync.Once
}

func NewMemoryRateLimiter(limit int, window time.Duration) *MemoryRateLimiter {
	l := &MemoryRateLimiter{
		counts: make(map[string]int),
		limit:  limit,
		window: window,
		done:   make(chan struct{}),
	}
	// 定期清理计数器
	go func() {
		ticker := time.NewTicker(window)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				l.mu.Lock()
				l.counts = make(map[string]int)
				l.mu.Unlock()
			case <-l.done:
				return
			}
		}
	}()
	return l
}

func (this *MemoryRateLimiter) Allow(ctx context.Context, key string) bool {
	this.mu.Lock()
	defer this.mu.Unlock()
	this.counts[key]++
	return this.counts[key] <= this.limit
}

func (this *MemoryRateLimiter) Stop() {
	this.stopOnce.Do(func() {
		close(this.done)
	})
}

func (this *MemoryRateLimiter) Name() string {
	return "MemoryRateLimiter"
}

// RedisRateLimiter 基于 Redis 的分布式限流器 (阶段 44 特性)
type RedisRateLimiter struct {
	Adapter *RedisAdapter
	Limit   int
	Window  time.Duration
	Prefix  string
}

func NewRedisRateLimiter(adapter *RedisAdapter, limit int, window time.Duration) *RedisRateLimiter {
	return &RedisRateLimiter{
		Adapter: adapter,
		Limit:   limit,
		Window:  window,
		Prefix:  "bear_limiter:",
	}
}

// Lua 脚本实现固定窗口限流
const luaIncr = `
local key = KEYS[1]
local limit = tonumber(ARGV[1])
local window = tonumber(ARGV[2])
local current = redis.call("INCR", key)
if current == 1 then
    redis.call("PEXPIRE", key, window)
end
if current > limit then
    return 0
end
return 1
`

func (this *RedisRateLimiter) Allow(ctx context.Context, key string) bool {
	if this.Adapter == nil || this.Adapter.Client == nil {
		return true // Fail-open
	}

	fullKey := this.Prefix + key
	// Window 以毫秒为单位传递给 Lua
	res, err := this.Adapter.Client.Eval(ctx, luaIncr, []string{fullKey}, this.Limit, this.Window.Milliseconds()).Int()
	if err != nil {
		slog.ErrorContext(ctx, "Redis RateLimiter error", "error", err)
		return true // Redis 异常时放行
	}

	return res == 1
}

func (this *RedisRateLimiter) Name() string {
	return "RedisRateLimiter"
}

// RateLimitMiddleware 限流中间件
func RateLimitMiddleware(limiter RateLimiter) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !limiter.Allow(c.Request.Context(), c.ClientIP()) {
			c.AbortWithStatusJSON(429, Error(429, "Too many requests (Distributed)"))
			return
		}
		c.Next()
	}
}

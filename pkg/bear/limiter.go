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

func (l *MemoryRateLimiter) Allow(ctx context.Context, key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.counts[key]++
	return l.counts[key] <= l.limit
}

func (l *MemoryRateLimiter) Stop() {
	l.stopOnce.Do(func() {
		close(l.done)
	})
}

func (l *MemoryRateLimiter) Name() string {
	return "MemoryRateLimiter"
}

// RedisRateLimiter 基于 Redis 的分布式限流器 (阶段 44 特性)
type RedisRateLimiter struct {
	Adapter    *RedisAdapter
	Limit      int
	Window     time.Duration
	Prefix     string
	FailClosed bool
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

func (l *RedisRateLimiter) Allow(ctx context.Context, key string) bool {
	if l.Adapter == nil || l.Adapter.Client == nil {
		return !l.FailClosed
	}

	fullKey := l.Prefix + key
	// Window 以毫秒为单位传递给 Lua
	res, err := l.Adapter.Client.Eval(ctx, luaIncr, []string{fullKey}, l.Limit, l.Window.Milliseconds()).Int()
	if err != nil {
		slog.ErrorContext(ctx, "Redis RateLimiter error", "error", err)
		return !l.FailClosed
	}

	return res == 1
}

func (l *RedisRateLimiter) Name() string {
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

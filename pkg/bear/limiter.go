package bear

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// RateLimiter 限流器接口
type RateLimiter interface {
	Allow(ctx context.Context, key string) bool
}

type LimiterFailureMode string

const (
	LimiterFailureModeOpen   LimiterFailureMode = "open"
	LimiterFailureModeClosed LimiterFailureMode = "closed"

	LimiterFailureOpen   = LimiterFailureModeOpen
	LimiterFailureClosed = LimiterFailureModeClosed
)

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
	if l.Validate() != nil {
		return l
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
	if l.Validate() != nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.counts[key]++
	return l.counts[key] <= l.limit
}

func (l *MemoryRateLimiter) Validate() error {
	if l == nil {
		return errors.New("memory rate limiter is nil")
	}
	return validateLimiterPolicy(l.limit, l.window)
}

func (l *MemoryRateLimiter) RetryAfter() time.Duration {
	if l == nil {
		return 0
	}
	return l.window
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
	Adapter     *RedisAdapter
	Limit       int
	Window      time.Duration
	Prefix      string
	FailClosed  bool
	FailureMode LimiterFailureMode
}

func NewRedisRateLimiter(adapter *RedisAdapter, limit int, window time.Duration) *RedisRateLimiter {
	return &RedisRateLimiter{
		Adapter:     adapter,
		Limit:       limit,
		Window:      window,
		Prefix:      "bear_limiter:",
		FailureMode: LimiterFailureModeOpen,
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
	if l.Validate() != nil {
		return false
	}
	if l.Adapter == nil || l.Adapter.Client == nil {
		return !l.failClosed()
	}

	fullKey := l.Prefix + key
	// Window 以毫秒为单位传递给 Lua，不能让正数窗口截断为零。
	res, err := l.Adapter.Client.Eval(ctx, luaIncr, []string{fullKey}, l.Limit, redisWindowMilliseconds(l.Window)).Int()
	if err != nil {
		slog.ErrorContext(ctx, "Redis RateLimiter error", "error", err)
		return !l.failClosed()
	}

	return res == 1
}

func (l *RedisRateLimiter) Validate() error {
	if l == nil {
		return errors.New("redis rate limiter is nil")
	}
	if err := validateLimiterPolicy(l.Limit, l.Window); err != nil {
		return err
	}
	switch l.FailureMode {
	case "", LimiterFailureModeOpen, LimiterFailureModeClosed:
		return nil
	default:
		return fmt.Errorf("limiter failure mode must be %q or %q", LimiterFailureModeOpen, LimiterFailureModeClosed)
	}
}

func (l *RedisRateLimiter) RetryAfter() time.Duration {
	if l == nil {
		return 0
	}
	return l.Window
}

func (l *RedisRateLimiter) failClosed() bool {
	return l.FailClosed || l.FailureMode == LimiterFailureModeClosed
}

func validateLimiterPolicy(limit int, window time.Duration) error {
	if limit <= 0 {
		return fmt.Errorf("rate limiter limit must be positive: %d", limit)
	}
	if window <= 0 {
		return fmt.Errorf("rate limiter window must be positive: %s", window)
	}
	return nil
}

func redisWindowMilliseconds(window time.Duration) int64 {
	milliseconds := window / time.Millisecond
	if window%time.Millisecond != 0 {
		milliseconds++
	}
	return int64(milliseconds)
}

func (l *RedisRateLimiter) Name() string {
	return "RedisRateLimiter"
}

// RateLimitMiddleware 限流中间件
func RateLimitMiddleware(limiter RateLimiter) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !limiter.Allow(c.Request.Context(), c.ClientIP()) {
			c.Header("Retry-After", strconv.FormatInt(retryAfterSeconds(limiter), 10))
			c.AbortWithStatusJSON(http.StatusTooManyRequests, Error(http.StatusTooManyRequests, "Too many requests (Distributed)"))
			return
		}
		c.Next()
	}
}

func retryAfterSeconds(limiter RateLimiter) int64 {
	delay := time.Second
	if provider, ok := limiter.(interface{ RetryAfter() time.Duration }); ok && provider.RetryAfter() > 0 {
		delay = provider.RetryAfter()
	}
	seconds := int64((delay + time.Second - 1) / time.Second)
	if seconds < 1 {
		return 1
	}
	return seconds
}

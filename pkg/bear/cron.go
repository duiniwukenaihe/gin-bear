package bear

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/robfig/cron/v3"
)

// CronManager 定时任务管理器
type CronManager struct {
	scheduler *cron.Cron
	redis     *RedisAdapter
	logger    *slog.Logger
}

func (m *CronManager) Name() string {
	return "CronManager"
}

// NewCronManager 创建定时任务管理器
// 需要注入 RedisAdapter 用于分布式锁
func NewCronManager(adapter *RedisAdapter) *CronManager {
	// 使用秒级精度 (cron.WithSeconds())
	// 使用自定义 Logger (cron.WithLogger)
	c := cron.New(cron.WithSeconds())

	return &CronManager{
		scheduler: c,
		redis:     adapter,
		logger:    slog.Default(),
	}
}

// Init 实现 Initializer 接口，自动启动调度器
func (m *CronManager) Init(ctx context.Context) error {
	m.scheduler.Start()
	m.logger.Info("Cron scheduler started")
	return nil
}

// Shutdown 实现 Shutdowner 接口，优雅停止
func (m *CronManager) Shutdown() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return m.ShutdownContext(ctx)
}

// ShutdownContext stops scheduling and waits for active jobs within ctx.
func (m *CronManager) ShutdownContext(ctx context.Context) error {
	if m == nil || m.scheduler == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	m.logger.Info("Stopping cron scheduler...")
	stopped := m.scheduler.Stop()
	select {
	case <-stopped.Done():
		m.logger.Info("Cron scheduler stopped gracefully")
		return nil
	case <-ctx.Done():
		m.logger.Warn("Cron scheduler stop timeout")
		return fmt.Errorf("cron scheduler shutdown: %w", ctx.Err())
	}
}

// AddFunc 添加普通任务 (单机执行，所有节点都会跑)
func (m *CronManager) AddFunc(spec string, cmd func()) (cron.EntryID, error) {
	return m.scheduler.AddFunc(spec, func() {
		// 简单的 panic 保护
		defer func() {
			if err := recover(); err != nil {
				m.logger.Error("Cron job panic", "error", err)
			}
		}()
		cmd()
	})
}

// AddDistributedFunc 添加分布式任务 (集群互斥，同一时刻只有一个节点跑)
// lockKey: 锁的唯一标识，通常是任务名
// ttl: 锁的过期时间，防止死锁 (必须大于任务执行时间)
func (m *CronManager) AddDistributedFunc(spec string, lockKey string, ttl time.Duration, cmd func()) (cron.EntryID, error) {
	if ttl <= 0 {
		return 0, fmt.Errorf("distributed cron lock TTL must be positive: %s", ttl)
	}
	return m.scheduler.AddFunc(spec, func() {
		ctx := context.Background()
		fullKey := "bear:cron:lock:" + lockKey

		if m.redis == nil || m.redis.Client == nil {
			m.logger.Error("Redis adapter not available for distributed job", "job", lockKey)
			return
		}

		lock, err := newOwnedCronLock(m.redis.Client, fullKey, ttl)
		if err != nil {
			m.logger.Error("Failed to generate distributed lock owner", "job", lockKey, "error", err)
			return
		}
		if err := lock.Acquire(ctx); errors.Is(err, ErrCronLockHeld) {
			m.logger.Debug("Distributed job skipped (lock held by another node)", "job", lockKey)
			return
		} else if err != nil {
			m.logger.Error("Failed to acquire distributed lock", "job", lockKey, "error", err)
			return
		}

		m.logger.Info("Distributed job started", "job", lockKey)
		warningTimer := time.AfterFunc(cronLockWarningDelay(ttl), func() {
			m.logger.Warn("Distributed job lock TTL nearing expiration",
				"job", lockKey,
				"ttl", ttl,
			)
		})
		defer func() {
			warningTimer.Stop()
			if recovered := recover(); recovered != nil {
				m.logger.Error("Distributed job panic", "job", lockKey, "error", recovered)
			}
			if err := lock.Release(ctx); err != nil {
				m.logger.Error("Failed to release distributed lock", "job", lockKey, "error", err)
			}
			m.logger.Info("Distributed job finished", "job", lockKey)
		}()

		cmd()
	})
}

func cronLockWarningDelay(ttl time.Duration) time.Duration {
	return ttl/5*4 + ttl%5*4/5
}

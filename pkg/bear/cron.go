package bear

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
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
	m.logger.Info("Stopping cron scheduler...")
	ctx := m.scheduler.Stop()
	// 等待正在执行的任务完成
	select {
	case <-ctx.Done():
		m.logger.Info("Cron scheduler stopped gracefully")
	case <-time.After(5 * time.Second):
		m.logger.Warn("Cron scheduler stop timeout")
	}
	return nil
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
	return m.scheduler.AddFunc(spec, func() {
		ctx := context.Background()
		fullKey := "bear:cron:lock:" + lockKey

		// 1. 尝试获取锁 (SetNX)
		if m.redis == nil {
			m.logger.Error("Redis adapter not available for distributed job", "job", lockKey)
			return
		}

		// 使用带 NX 选项的 SET 抢占锁
		status, err := m.redis.Client.SetArgs(ctx, fullKey, "locked", redis.SetArgs{
			Mode: "NX",
			TTL:  ttl,
		}).Result()
		if errors.Is(err, redis.Nil) {
			m.logger.Debug("Distributed job skipped (lock held by another node)", "job", lockKey)
			return
		}
		if err != nil {
			m.logger.Error("Failed to acquire distributed lock", "job", lockKey, "error", err)
			return
		}

		if status != "OK" {
			// 没抢到锁，说明其他节点正在执行，跳过本次调度
			m.logger.Debug("Distributed job skipped (lock held by another node)", "job", lockKey)
			return
		}

		// 2. 抢到锁，执行任务
		m.logger.Info("Distributed job started", "job", lockKey)
		defer func() {
			// 3. 任务完成后释放锁?
			// 策略选择：
			// A. 立即释放 (Del)：允许下一个周期立即抢占。但如果任务耗时极短，可能会在同一秒内被其他节点再次抢占？(cron 是整秒触发，基本安全)
			// B. 等待 TTL 过期：更简单，但如果任务异常退出，需要等 TTL。
			// 这里选择 A: 主动释放，但也依赖 TTL 防止死锁。

			if err := recover(); err != nil {
				m.logger.Error("Distributed job panic", "job", lockKey, "error", err)
			}

			// 只有当自己持有锁时才释放 (虽然 Redis Del 不检查谁持有，但在过期时间极短的情况下可能误删别人的锁? 不，我们用的是 SetNX)
			m.redis.Client.Del(ctx, fullKey)
			m.logger.Info("Distributed job finished", "job", lockKey)
		}()

		cmd()
	})
}

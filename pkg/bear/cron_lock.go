package bear

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

var ErrCronLockHeld = errors.New("cron lock is held by another owner")

const releaseOwnedLock = `
if redis.call("GET", KEYS[1]) == ARGV[1] then
  return redis.call("DEL", KEYS[1])
end
return 0`

type cronLock struct {
	client *redis.Client
	key    string
	owner  string
	ttl    time.Duration
}

func newCronLock(client *redis.Client, key, owner string, ttl time.Duration) *cronLock {
	return &cronLock{client: client, key: key, owner: owner, ttl: ttl}
}

func newOwnedCronLock(client *redis.Client, key string, ttl time.Duration) (*cronLock, error) {
	ownerBytes := make([]byte, 16)
	if _, err := rand.Read(ownerBytes); err != nil {
		return nil, fmt.Errorf("generate cron lock owner: %w", err)
	}
	return newCronLock(client, key, hex.EncodeToString(ownerBytes), ttl), nil
}

func (l *cronLock) Acquire(ctx context.Context) error {
	if l == nil || l.client == nil {
		return errors.New("cron lock requires a Redis client")
	}
	if l.ttl <= 0 {
		return fmt.Errorf("cron lock TTL must be positive: %s", l.ttl)
	}
	_, err := l.client.SetArgs(ctx, l.key, l.owner, redis.SetArgs{Mode: "NX", TTL: l.ttl}).Result()
	if errors.Is(err, redis.Nil) {
		return ErrCronLockHeld
	}
	if err != nil {
		return err
	}
	return nil
}

func (l *cronLock) Release(ctx context.Context) error {
	if l == nil || l.client == nil {
		return errors.New("cron lock requires a Redis client")
	}
	return l.client.Eval(ctx, releaseOwnedLock, []string{l.key}, l.owner).Err()
}

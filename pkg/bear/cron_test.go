package bear

import (
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/robfig/cron/v3"
)

func TestDistributedCronJobAcquiresLockAndExecutes(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	manager := NewCronManager(&RedisAdapter{Client: client})
	runs := 0
	id, err := manager.AddDistributedFunc("* * * * * *", "sync", time.Hour, func() {
		runs++
	})
	if err != nil {
		t.Fatalf("AddDistributedFunc() error = %v", err)
	}

	runCronEntry(t, manager, id)

	if runs != 1 {
		t.Fatalf("job ran %d times, want 1", runs)
	}
	if server.Exists("bear:cron:lock:sync") {
		t.Fatal("expected completed job to release its Redis lock")
	}
}

func TestDistributedCronJobSkipsWhenLockIsHeld(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	const lock = "bear:cron:lock:sync"
	if err := server.Set(lock, "held"); err != nil {
		t.Fatal(err)
	}

	manager := NewCronManager(&RedisAdapter{Client: client})
	runs := 0
	id, err := manager.AddDistributedFunc("* * * * * *", "sync", time.Hour, func() {
		runs++
	})
	if err != nil {
		t.Fatalf("AddDistributedFunc() error = %v", err)
	}

	runCronEntry(t, manager, id)

	if runs != 0 {
		t.Fatalf("job ran %d times while lock was held", runs)
	}
	if got, err := server.Get(lock); err != nil || got != "held" {
		t.Fatalf("lock after skipped job = %q, %v", got, err)
	}
}

func runCronEntry(t *testing.T, manager *CronManager, id cron.EntryID) {
	t.Helper()
	for _, entry := range manager.scheduler.Entries() {
		if entry.ID == id {
			entry.Job.Run()
			return
		}
	}
	t.Fatalf("scheduled job %d not found", id)
}

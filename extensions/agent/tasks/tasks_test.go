package tasks

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/glebarez/sqlite"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func openSQLite(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tasks.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	up, err := MigrationUp("sqlite")
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range splitStatements(up) {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("migrate up: %v\n%s", err, stmt)
		}
	}
	store, err := Open(db, "sqlite")
	if err != nil {
		t.Fatal(err)
	}
	return store
}

// openPostgres runs the same store against a disposable database when the
// harness provides one; otherwise the variant skips as NOT_RUN.
func openPostgres(t *testing.T) (*Store, bool) {
	t.Helper()
	dsn := os.Getenv("BEAR_AGENT_PG_DSN")
	if dsn == "" {
		t.Skip("NOT_RUN: no BEAR_AGENT_PG_DSN")
		return nil, false
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	up, err := MigrationUp("postgres")
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range splitStatements(up) {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("migrate up: %v\n%s", err, stmt)
		}
	}
	t.Cleanup(func() {
		down, _ := MigrationDown("postgres")
		_, _ = db.Exec(down)
	})
	store, err := Open(db, "postgres")
	if err != nil {
		t.Fatal(err)
	}
	return store, true
}

func submitReadonly(t *testing.T, store *Store, tenant, key string) *Task {
	t.Helper()
	task, err := store.Submit(context.Background(), Task{
		TenantID: tenant, Owner: "u1", IdempotencyKey: key,
		Kind: "readonly", Tool: "get_order", Args: `{"order_id":"1"}`,
		BudgetTokens: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func okExecutor(result string) func(context.Context, *Task) Outcome {
	return func(context.Context, *Task) Outcome { return Outcome{Result: result} }
}

func TestSubmitIsIdempotent(t *testing.T) {
	store := openSQLite(t)
	ctx := context.Background()
	first := submitReadonly(t, store, "t1", "k1")
	second := submitReadonly(t, store, "t1", "k1")
	if first.ID != second.ID {
		t.Fatalf("duplicate submit created %s and %s", first.ID, second.ID)
	}
	other, err := store.Submit(ctx, Task{TenantID: "t1", Owner: "u1", IdempotencyKey: "k2", Kind: "readonly", Tool: "x", Args: `{}`})
	if err != nil || other.ID == first.ID {
		t.Fatalf("distinct key returned same task: %v %+v", err, other)
	}
	foreign, err := store.Submit(ctx, Task{TenantID: "t2", Owner: "u1", IdempotencyKey: "k1", Kind: "readonly", Tool: "x", Args: `{}`})
	if err != nil || foreign.ID == first.ID {
		t.Fatalf("tenant isolation broken: %v %+v", err, foreign)
	}
}

func TestConcurrentDuplicateSubmitWinsOnce(t *testing.T) {
	store := openSQLite(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	ids := make(chan string, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			task, err := store.Submit(ctx, Task{TenantID: "t1", Owner: "u1", IdempotencyKey: "race", Kind: "readonly", Tool: "x", Args: `{}`})
			if err != nil {
				t.Errorf("submit: %v", err)
				return
			}
			ids <- task.ID
		}()
	}
	wg.Wait()
	close(ids)
	seen := map[string]bool{}
	for id := range ids {
		seen[id] = true
	}
	if len(seen) != 1 {
		t.Fatalf("concurrent submits created %d tasks", len(seen))
	}
}

func TestReadonlyClaimExecuteSucceeds(t *testing.T) {
	store := openSQLite(t)
	ctx := context.Background()
	submitReadonly(t, store, "t1", "k1")
	worker := &Worker{Store: store, Owner: "w1", Executor: okExecutor("done")}
	finished, err := worker.RunOnce(ctx)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if finished.Status != StatusSucceeded || finished.Result != "done" {
		t.Fatalf("task = %+v", finished)
	}
	if _, err := worker.RunOnce(ctx); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("second RunOnce = %v, want idle", err)
	}
}

func TestFencingRejectsStaleWorker(t *testing.T) {
	store := openSQLite(t)
	ctx := context.Background()
	submitReadonly(t, store, "t1", "k1")
	claimed, err := store.Claim(ctx, "w1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Complete(ctx, claimed.ID, claimed.Version-1, Outcome{Result: "stale"}); err == nil {
		t.Fatal("stale complete accepted")
	}
	fresh, err := store.Get(ctx, claimed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Status != StatusRunning || fresh.Result != "" {
		t.Fatalf("stale complete mutated task: %+v", fresh)
	}
	if _, err := store.Complete(ctx, claimed.ID, claimed.Version, Outcome{Result: "ok"}); err != nil {
		t.Fatalf("fresh complete: %v", err)
	}
}

func approveTask(t *testing.T, store *Store, task *Task, operator string) *Task {
	t.Helper()
	approved, err := store.Approve(context.Background(), task.ApprovalID, operator, task.Args)
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	return approved
}

func submitWrite(t *testing.T, store *Store, tenant, key string) *Task {
	t.Helper()
	task, err := store.Submit(context.Background(), Task{
		TenantID: tenant, Owner: "u1", IdempotencyKey: key,
		Kind: "write", Tool: "update_order", Args: `{"order_id":"1"}`,
		BudgetTokens: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != StatusWaiting || task.ApprovalID == "" {
		t.Fatalf("write submit = %+v, want waiting_approval with approval", task)
	}
	return task
}

func TestWriteApprovalSingleUseExpiryAndHash(t *testing.T) {
	store := openSQLite(t)
	ctx := context.Background()
	task := submitWrite(t, store, "t1", "w1")

	if _, err := store.Approve(ctx, task.ApprovalID, "op", `{"order_id":"2"}`); err == nil {
		t.Fatal("changed parameters approved")
	}
	approved := approveTask(t, store, task, "op1")
	if approved.Status != StatusQueued {
		t.Fatalf("approved task = %s, want queued", approved.Status)
	}
	if _, err := store.Approve(ctx, task.ApprovalID, "op2", task.Args); err == nil {
		t.Fatal("consumed approval reused")
	}

	other := submitWrite(t, store, "t1", "w2")
	// Expire it directly, then approval must refuse.
	store.mu.Lock()
	_, err := store.db.Exec(`UPDATE agent_approvals SET expires_at = ? WHERE id = ?`, now()-1, other.ApprovalID)
	store.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Approve(ctx, other.ApprovalID, "op", other.Args); err == nil {
		t.Fatal("expired approval accepted")
	}
}

func TestWriteExecutesAfterApprovalAndRespectsPolicyBump(t *testing.T) {
	store := openSQLite(t)
	ctx := context.Background()
	store.PolicyVersion = "v3"
	task := submitWrite(t, store, "t1", "w1")
	approveTask(t, store, task, "op")

	worker := &Worker{Store: store, Owner: "w1", Executor: okExecutor("wrote")}
	finished, err := worker.RunOnce(ctx)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if finished.Status != StatusSucceeded {
		t.Fatalf("task = %+v", finished)
	}

	// Revocation bumps the policy generation: old approvals stop working.
	second := submitWrite(t, store, "t1", "w2")
	approveTask(t, store, second, "op")
	store.PolicyVersion = "v4"
	finished, err = worker.RunOnce(ctx)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if finished.Status != StatusFailed || !strings.Contains(finished.ErrorText, "policy changed") {
		t.Fatalf("post-revocation task = %+v, want policy refusal", finished)
	}
	// Re-approval under the new generation runs.
	third := submitWrite(t, store, "t1", "w3")
	approveTask(t, store, third, "op")
	finished, err = worker.RunOnce(ctx)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if finished.Status != StatusSucceeded {
		t.Fatalf("re-approved task = %+v", finished)
	}
}

func TestCancelStopsNewEffects(t *testing.T) {
	store := openSQLite(t)
	ctx := context.Background()
	queued := submitReadonly(t, store, "t1", "k1")
	canceled, err := store.Cancel(ctx, queued.ID)
	if err != nil || canceled.Status != StatusCanceled {
		t.Fatalf("cancel queued = %+v, %v", canceled, err)
	}
	if _, err := store.Claim(ctx, "w1", time.Minute); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("canceled task claimed: %v", err)
	}
	submitReadonly(t, store, "t1", "k2")
	claimed, err := store.Claim(ctx, "w1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	canceled, err = store.Cancel(ctx, claimed.ID)
	if err != nil || canceled.Status != StatusCanceled {
		t.Fatalf("cancel running = %+v, %v", canceled, err)
	}
	again, err := store.Cancel(ctx, claimed.ID)
	if err != nil || again.Status != StatusCanceled {
		t.Fatalf("second cancel = %+v, %v", again, err)
	}
}

func TestTimeoutsUnknownRetryAndAttempts(t *testing.T) {
	store := openSQLite(t)
	ctx := context.Background()

	expiring := submitReadonly(t, store, "t1", "exp")
	store.mu.Lock()
	_, err := store.db.Exec(`UPDATE agent_tasks SET expires_at = ? WHERE id = ?`, now()-1, expiring.ID)
	store.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Claim(ctx, "w1", time.Minute); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expired task claimed: %v", err)
	}
	if swept, err := store.SweepTimeouts(ctx); err != nil || swept != 1 {
		t.Fatalf("sweep = %d, %v; want 1, nil", swept, err)
	}
	got, _ := store.Get(ctx, expiring.ID)
	if got.Status != StatusTimedOut {
		t.Fatalf("expired task = %s, want timed_out", got.Status)
	}

	submitReadonly(t, store, "t1", "myst")
	worker := &Worker{Store: store, Owner: "w1", Executor: func(context.Context, *Task) Outcome {
		return Outcome{Err: ErrUnknownResult}
	}}
	finished, err := worker.RunOnce(ctx)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if finished.Status != StatusUnknown || finished.Attempts != 1 {
		t.Fatalf("unknown task = %+v, want unknown without retry", finished)
	}

	submitReadonly(t, store, "t1", "flaky")
	retryWorker := &Worker{Store: store, Owner: "w1", Executor: func(context.Context, *Task) Outcome {
		return Outcome{Err: errors.New("transient"), Retryable: true}
	}}
	for i := 0; i < 3; i++ {
		finished, err = retryWorker.RunOnce(ctx)
		if err != nil {
			t.Fatalf("retry run %d: %v", i, err)
		}
	}
	if finished.Status != StatusFailed || finished.Attempts != 3 {
		t.Fatalf("retried task = %+v, want failed after 3 attempts", finished)
	}
}

func TestCrashHooksPreserveState(t *testing.T) {
	store := openSQLite(t)
	ctx := context.Background()
	submitReadonly(t, store, "t1", "k1")
	worker := &Worker{Store: store, Owner: "w1", Executor: okExecutor("x")}

	store.Hooks.BeforeExecute = func(*Task) error { return errors.New("crash before execute") }
	crashed, err := worker.RunOnce(ctx)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if crashed.Attempts != 1 || crashed.Status != StatusQueued {
		t.Fatalf("crashed task = %+v, want requeued after 1 attempt", crashed)
	}
	store.Hooks.BeforeExecute = nil

	store.Hooks.BeforeStore = func(*Task) error { return errors.New("crash before store") }
	if _, err := worker.RunOnce(ctx); err == nil {
		t.Fatal("store crash swallowed")
	}
	store.Hooks.BeforeStore = nil
	// White-box lease expiry: the crashed worker never renewed it.
	expireLease(t, store, crashed.ID)
	recovered, err := store.Recover(ctx)
	if err != nil || recovered != 1 {
		t.Fatalf("recover = %d, %v; want 1, nil", recovered, err)
	}
	leftover, err := store.Claim(ctx, "w1", time.Minute)
	if err != nil {
		t.Fatalf("task lost after store crash: %v", err)
	}
	if _, err := store.Complete(ctx, leftover.ID, leftover.Version, Outcome{Result: "recovered"}); err != nil {
		t.Fatalf("recover complete: %v", err)
	}
}

func expireLease(t *testing.T, store *Store, id string) {
	t.Helper()
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, err := store.db.Exec(`UPDATE agent_tasks SET lease_expires = ? WHERE id = ?`, now()-1, id); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryPreservesBudget(t *testing.T) {
	store := openSQLite(t)
	ctx := context.Background()
	submitReadonly(t, store, "t1", "k1")
	claimed, err := store.Claim(ctx, "w1", -time.Second)
	if err != nil {
		t.Fatalf("claim with expired lease: %v", err)
	}
	if _, err := store.Complete(ctx, claimed.ID, claimed.Version, Outcome{Result: "half", Usage: 30}); err != nil {
		t.Fatal(err)
	}
	before, _ := store.Get(ctx, claimed.ID)
	if before.BudgetTokens != 970 {
		t.Fatalf("budget = %d, want 970 after usage charge", before.BudgetTokens)
	}
	// A second task crashes mid-run (no Complete): recovery requeues it with
	// the budget untouched, never re-issued.
	submitReadonly(t, store, "t1", "k2")
	crashed, err := store.Claim(ctx, "w2", -time.Second)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := store.Recover(ctx)
	if err != nil || recovered != 1 {
		t.Fatalf("recover = %d, %v; want 1, nil", recovered, err)
	}
	after, _ := store.Get(ctx, crashed.ID)
	if after.Status != StatusQueued || after.BudgetTokens != 1000 {
		t.Fatalf("recovered task = %+v, want queued with budget 1000", after)
	}
}

func TestMigrationUpDownReviewed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mig.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	up, err := MigrationUp("sqlite")
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range splitStatements(up) {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("up statement failed: %v\n%s", err, stmt)
		}
	}
	var name string
	if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='agent_tasks'`).Scan(&name); err != nil || name != "agent_tasks" {
		t.Fatalf("agent_tasks missing: %v", err)
	}
	down, err := MigrationDown("sqlite")
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range splitStatements(down) {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("down statement failed: %v\n%s", err, stmt)
		}
	}
	if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='agent_tasks'`).Scan(&name); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("agent_tasks survived down: %v", err)
	}
	if _, err := MigrationUp("oracle"); err == nil {
		t.Fatal("unsupported dialect accepted")
	}
}

func splitStatements(script string) []string {
	var statements []string
	for _, stmt := range strings.Split(script, ";") {
		if trimmed := strings.TrimSpace(stmt); trimmed != "" {
			statements = append(statements, trimmed)
		}
	}
	return statements
}

func TestPostgresVariant(t *testing.T) {
	store, ok := openPostgres(t)
	if !ok {
		return
	}
	ctx := context.Background()
	task, err := store.Submit(ctx, Task{TenantID: "t1", Owner: "u1", IdempotencyKey: "pg1", Kind: "write", Tool: "update_order", Args: `{"order_id":"1"}`, BudgetTokens: 100})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if task.Status != StatusWaiting {
		t.Fatalf("task = %s, want waiting_approval", task.Status)
	}
	approved, err := store.Approve(ctx, task.ApprovalID, "op", task.Args)
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	worker := &Worker{Store: store, Owner: "w1", Executor: okExecutor("pg-done")}
	finished, err := worker.RunOnce(ctx)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if finished.Status != StatusSucceeded || finished.Result != "pg-done" {
		t.Fatalf("task = %+v", finished)
	}
	_ = approved
}

// openPair returns two independent Stores over two connections to one file:
// the closest hermetic approximation of two instances. Busy-timeout and WAL
// make SQLite writers block like PostgreSQL row locks instead of failing
// with SQLITE_BUSY, so the race tests observe fencing outcomes rather than
// driver lock flakes.
func openPair(t *testing.T) (*Store, *Store) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pair.db")
	open := func() *sql.DB {
		db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		return db
	}
	first, second := open(), open()
	up, err := MigrationUp("sqlite")
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range splitStatements(up) {
		if _, err := first.Exec(stmt); err != nil {
			t.Fatalf("migrate: %v", err)
		}
	}
	a, err := Open(first, "sqlite")
	if err != nil {
		t.Fatal(err)
	}
	b, err := Open(second, "sqlite")
	if err != nil {
		t.Fatal(err)
	}
	return a, b
}

// TestCrossInstanceClaimFencing proves two independent Stores racing for one
// row produce exactly one winner, and that cancel beats a late complete.
func TestCrossInstanceClaimFencing(t *testing.T) {
	a, b := openPair(t)
	ctx := context.Background()
	submitted, err := a.Submit(ctx, Task{TenantID: "t1", Owner: "u", IdempotencyKey: "k1", Kind: "readonly", Tool: "x", Args: `{}`})
	if err != nil {
		t.Fatal(err)
	}

	claimedA, err := a.Claim(ctx, "w1", time.Minute)
	if err != nil {
		t.Fatalf("A claim: %v", err)
	}
	if _, err := b.Claim(ctx, "w2", time.Minute); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("B claim during A lease = %v, want idle", err)
	}

	// B never held the lease: completing with a guessed old version is stale.
	if _, err := b.Complete(ctx, submitted.ID, submitted.Version, Outcome{Result: "late"}); err == nil {
		t.Fatal("stale cross-instance complete accepted")
	}

	// Cancel wins over the in-flight claim.
	canceled, err := b.Cancel(ctx, submitted.ID)
	if err != nil || canceled.Status != StatusCanceled {
		t.Fatalf("cancel = %+v, %v", canceled, err)
	}
	late, err := a.Complete(ctx, claimedA.ID, claimedA.Version, Outcome{Result: "too late"})
	if err != nil {
		t.Fatalf("late complete: %v", err)
	}
	if late.Status != StatusCanceled || late.Result != "" {
		t.Fatalf("cancel lost to late complete: %+v", late)
	}
}

// TestCrossInstanceClaimStorm races two Stores over many tasks: every task
// executes exactly once and nothing stays running.
func TestCrossInstanceClaimStorm(t *testing.T) {
	a, b := openPair(t)
	ctx := context.Background()
	const tasks = 12
	for i := 0; i < tasks; i++ {
		if _, err := a.Submit(ctx, Task{TenantID: "t1", Owner: "u", IdempotencyKey: fmt.Sprintf("storm-%d", i), Kind: "readonly", Tool: "x", Args: `{}`}); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	effects := map[string]int{}
	// SQLite file locks serialize writers; brief "database is locked"
	// contention is retried, genuine errors fail the test.
	retryable := func(op func() error) error {
		var err error
		for i := 0; i < 50; i++ {
			if err = op(); err == nil {
				return nil
			}
			if !strings.Contains(err.Error(), "locked") {
				return err
			}
			time.Sleep(time.Duration(i+1) * time.Millisecond)
		}
		return err
	}
	work := func(store *Store, owner string, wg *sync.WaitGroup) {
		defer wg.Done()
		for {
			var claimed *Task
			if err := retryable(func() error {
				var err error
				claimed, err = store.Claim(ctx, owner, time.Minute)
				return err
			}); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return
				}
				t.Errorf("claim: %v", err)
				return
			}
			mu.Lock()
			effects[claimed.ID]++
			mu.Unlock()
			if err := retryable(func() error {
				_, err := store.Complete(ctx, claimed.ID, claimed.Version, Outcome{Result: "ok"})
				return err
			}); err != nil {
				t.Errorf("complete: %v", err)
				return
			}
		}
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go work(a, "w1", &wg)
	go work(b, "w2", &wg)
	wg.Wait()
	if len(effects) != tasks {
		t.Fatalf("claimed %d distinct tasks, want %d", len(effects), tasks)
	}
	for id, count := range effects {
		if count != 1 {
			t.Fatalf("task %s executed %d times", id, count)
		}
	}
}

// TestCrossInstanceLeaseRecovery proves an expired lease is recoverable by
// the other instance while the stale owner's late complete is refused.
func TestCrossInstanceLeaseRecovery(t *testing.T) {
	a, b := openPair(t)
	ctx := context.Background()
	submitted, err := a.Submit(ctx, Task{TenantID: "t1", Owner: "u", IdempotencyKey: "k1", Kind: "readonly", Tool: "x", Args: `{}`})
	if err != nil {
		t.Fatal(err)
	}
	claimedA, err := a.Claim(ctx, "w1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	expireLease(t, a, claimedA.ID)
	recovered, err := b.Recover(ctx)
	if err != nil || recovered != 1 {
		t.Fatalf("recover = %d, %v", recovered, err)
	}
	claimedB, err := b.Claim(ctx, "w2", time.Minute)
	if err != nil {
		t.Fatalf("B claim after recovery: %v", err)
	}
	if _, err := a.Complete(ctx, submitted.ID, claimedA.Version, Outcome{Result: "stale"}); err == nil {
		t.Fatal("stale owner completed after lease recovery")
	}
	if _, err := b.Complete(ctx, claimedB.ID, claimedB.Version, Outcome{Result: "ok"}); err != nil {
		t.Fatalf("B complete: %v", err)
	}
	finished, err := b.Get(ctx, submitted.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finished.Status != StatusSucceeded || finished.Result != "ok" {
		t.Fatalf("task = %+v", finished)
	}
}

// TestWriteCrashReconcilesInsteadOfRetrying is the R2 acceptance: external
// success followed by a pre-commit crash must surface as unknown with the
// effect recorded exactly once, never as a silent requeue.
func TestWriteCrashReconcilesInsteadOfRetrying(t *testing.T) {
	store := openSQLite(t)
	ctx := context.Background()
	task := submitWrite(t, store, "t1", "w1")
	approveTask(t, store, task, "op")

	effects := map[string]int{}
	worker := &Worker{Store: store, Owner: "w1", WriteReserveTokens: 200, Executor: func(_ context.Context, task *Task) Outcome {
		effects[task.ExecNonce]++
		return Outcome{Result: "wrote", Usage: 60}
	}}
	store.Hooks.BeforeStore = func(*Task) error { return errors.New("crash before commit") }
	if _, err := worker.RunOnce(ctx); err == nil {
		t.Fatal("commit crash swallowed")
	}
	store.Hooks.BeforeStore = nil

	claimed, err := store.Get(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.ReservedTokens != 200 || claimed.BudgetTokens != 800 || claimed.ExecNonce == "" {
		t.Fatalf("intent not persisted: %+v", claimed)
	}
	expireLease(t, store, task.ID)
	moved, err := store.Recover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if moved != 1 {
		t.Fatalf("recover moved %d, want 1", moved)
	}
	after, err := store.Get(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != StatusUnknown {
		t.Fatalf("crashed write = %s, want unknown (not a blind requeue)", after.Status)
	}
	if after.ReservedTokens != 200 || after.BudgetTokens != 800 {
		t.Fatalf("reservation lost across crash: %+v", after)
	}
	// A second worker finds nothing to run; the effect happened exactly once.
	if _, err := worker.RunOnce(ctx); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("unknown task reran: %v", err)
	}
	if len(effects) != 1 {
		t.Fatalf("effects = %v, want exactly one nonce", effects)
	}
	for nonce, count := range effects {
		if nonce == "" || count != 1 {
			t.Fatalf("nonce %q executed %d times", nonce, count)
		}
	}

	// Out-of-band verification confirms the write: exact settlement against
	// the surviving reservation.
	confirmed, err := store.ConfirmUnknown(ctx, task.ID, "wrote", 60)
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if confirmed.Status != StatusSucceeded || confirmed.BudgetTokens != 940 || confirmed.ReservedTokens != 0 {
		t.Fatalf("confirmed = %+v, want succeeded with budget 940", confirmed)
	}
}

// TestDefaultWorkerWriteCrashReconcilesInsteadOfRetrying is the V1
// acceptance: a worker WITHOUT a budget reservation must still persist the
// execution intent, so a crash between external success and commit surfaces
// as unknown — never as a silent requeue that re-executes the write.
func TestDefaultWorkerWriteCrashReconcilesInsteadOfRetrying(t *testing.T) {
	store := openSQLite(t)
	ctx := context.Background()
	task := submitWrite(t, store, "t1", "w1")
	approveTask(t, store, task, "op")

	effects := map[string]int{}
	worker := &Worker{Store: store, Owner: "w1", Executor: func(_ context.Context, task *Task) Outcome {
		effects[task.ExecNonce]++
		return Outcome{Result: "wrote", Usage: 60}
	}}
	store.Hooks.BeforeStore = func(*Task) error { return errors.New("crash before commit") }
	if _, err := worker.RunOnce(ctx); err == nil {
		t.Fatal("commit crash swallowed")
	}
	store.Hooks.BeforeStore = nil

	claimed, err := store.Get(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.ExecNonce == "" {
		t.Fatalf("default worker persisted no intent: %+v", claimed)
	}
	if claimed.ReservedTokens != 0 || claimed.BudgetTokens != 1000 {
		t.Fatalf("zero-reserve run moved budget: %+v", claimed)
	}
	expireLease(t, store, task.ID)
	moved, err := store.Recover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if moved != 1 {
		t.Fatalf("recover moved %d, want 1", moved)
	}
	after, err := store.Get(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != StatusUnknown {
		t.Fatalf("crashed write = %s, want unknown (not a blind requeue)", after.Status)
	}
	// A second worker finds nothing to run; the effect happened exactly once.
	if _, err := worker.RunOnce(ctx); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("unknown task reran: %v", err)
	}
	if len(effects) != 1 {
		t.Fatalf("effects = %v, want exactly one nonce", effects)
	}
	for nonce, count := range effects {
		if nonce == "" || count != 1 {
			t.Fatalf("nonce %q executed %d times", nonce, count)
		}
	}
	// Reconciliation without a usage report keeps the untouched budget.
	confirmed, err := store.ConfirmUnknown(ctx, task.ID, "wrote", 60)
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if confirmed.Status != StatusSucceeded || confirmed.BudgetTokens != 940 {
		t.Fatalf("confirmed = %+v, want succeeded with budget 940", confirmed)
	}
}

// TestUnknownRequeueRequiresFreshApproval proves requeue refunds the
// reservation, retires the consumed approval, and demands a new one.
func TestUnknownRequeueRequiresFreshApproval(t *testing.T) {
	store := openSQLite(t)
	ctx := context.Background()
	task := submitWrite(t, store, "t1", "w1")
	approveTask(t, store, task, "op")
	var executions int
	worker := &Worker{Store: store, Owner: "w1", WriteReserveTokens: 200, Executor: func(context.Context, *Task) Outcome {
		executions++
		return Outcome{Result: "wrote"}
	}}
	store.Hooks.BeforeStore = func(*Task) error { return errors.New("crash") }
	if _, err := worker.RunOnce(ctx); err == nil {
		t.Fatal("crash swallowed")
	}
	store.Hooks.BeforeStore = nil
	expireLease(t, store, task.ID)
	if _, err := store.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	before, _ := store.Get(ctx, task.ID)
	if before.Status != StatusUnknown {
		t.Fatalf("status = %s, want unknown", before.Status)
	}
	oldApproval := before.ApprovalID

	requeued, err := store.RequeueUnknown(ctx, task.ID)
	if err != nil {
		t.Fatalf("requeue: %v", err)
	}
	if requeued.Status != StatusWaiting || requeued.ApprovalID == "" || requeued.ApprovalID == oldApproval {
		t.Fatalf("requeued = %+v, want waiting with a fresh approval", requeued)
	}
	if requeued.BudgetTokens != 1000 || requeued.ReservedTokens != 0 {
		t.Fatalf("reservation not refunded: %+v", requeued)
	}
	if _, err := worker.RunOnce(ctx); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("unapproved requeue ran: %v", err)
	}
	if executions != 1 {
		t.Fatalf("crashed attempt executed %d times, want exactly 1", executions)
	}
	approveTask(t, store, requeued, "op2")
	finished, err := worker.RunOnce(ctx)
	if err != nil {
		t.Fatalf("run after re-approval: %v", err)
	}
	// The re-approved re-execution is an explicit operator decision after
	// reconciliation, not a blind retry.
	if finished.Status != StatusSucceeded || executions != 2 {
		t.Fatalf("finished = %+v executions=%d", finished, executions)
	}
}

// countApprovals reports live approval rows for orphan detection.
func countApprovals(t *testing.T, store *Store) int {
	t.Helper()
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM agent_approvals`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func approvalConsumed(t *testing.T, store *Store, approvalID string) bool {
	t.Helper()
	var consumed int
	if err := store.db.QueryRow(`SELECT consumed FROM agent_approvals WHERE id = ?`, approvalID).Scan(&consumed); err != nil {
		t.Fatal(err)
	}
	return consumed != 0
}

// TestConcurrentSubmitLeavesNoOrphanApproval is the first approval-atomicity
// acceptance: two instances submitting the same write key produce one task
// and one approval. The loser's insert fails inside the submit transaction,
// so its approval rolls back instead of lingering as an orphan.
func TestConcurrentSubmitLeavesNoOrphanApproval(t *testing.T) {
	a, b := openPair(t)
	ctx := context.Background()
	submit := func(store *Store) *Task {
		task, err := store.Submit(ctx, Task{TenantID: "t1", Owner: "u1", IdempotencyKey: "race-write", Kind: "write", Tool: "update_order", Args: `{"order_id":"1"}`, BudgetTokens: 1000})
		if err != nil {
			t.Errorf("submit: %v", err)
			return nil
		}
		return task
	}
	var first, second *Task
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); first = submit(a) }()
	go func() { defer wg.Done(); second = submit(b) }()
	wg.Wait()
	if first == nil || second == nil {
		t.Fatal("concurrent submit failed")
	}
	if first.ID != second.ID {
		t.Fatalf("duplicate write tasks %s and %s", first.ID, second.ID)
	}
	if countApprovals(t, a) != 1 {
		t.Fatalf("approvals leaked: want exactly 1 row for one task")
	}
	winner, err := a.Get(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if winner.ApprovalID == "" || winner.Status != StatusWaiting {
		t.Fatalf("winner = %+v, want waiting with an approval", winner)
	}
}

// TestApprovalCrashRollsBackConsume is the second acceptance: a crash after
// the consume write but before commit rolls the whole approval back. The
// approval stays unconsumed, the task stays waiting, and a retry succeeds.
func TestApprovalCrashRollsBackConsume(t *testing.T) {
	store := openSQLite(t)
	ctx := context.Background()
	task := submitWrite(t, store, "t1", "w1")

	store.Hooks.BeforeApprovalCommit = func(*Task) error { return errors.New("crash before approval commit") }
	if _, err := store.Approve(ctx, task.ApprovalID, "op", task.Args); err == nil {
		t.Fatal("approval crash swallowed")
	}
	store.Hooks.BeforeApprovalCommit = nil
	if approvalConsumed(t, store, task.ApprovalID) {
		t.Fatal("crashed approval shows consumed: task is stranded")
	}
	waiting, err := store.Get(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if waiting.Status != StatusWaiting {
		t.Fatalf("task = %s, want waiting after rolled-back approval", waiting.Status)
	}
	approved, err := store.Approve(ctx, task.ApprovalID, "op", task.Args)
	if err != nil {
		t.Fatalf("retry after rollback: %v", err)
	}
	if approved.Status != StatusQueued {
		t.Fatalf("retried task = %s, want queued", approved.Status)
	}
}

// TestConcurrentApproveAndRequeueSingleWinner is the third acceptance: two
// independent connections racing Approve produce exactly one winner, racing
// RequeueUnknown produce exactly one fresh approval, and a Cancel racing
// Approve wins without stranding a consumed approval.
func TestConcurrentApproveAndRequeueSingleWinner(t *testing.T) {
	t.Run("approve", func(t *testing.T) {
		a, b := openPair(t)
		ctx := context.Background()
		task, err := a.Submit(ctx, Task{TenantID: "t1", Owner: "u1", IdempotencyKey: "k-approve", Kind: "write", Tool: "update_order", Args: `{"order_id":"1"}`, BudgetTokens: 1000})
		if err != nil {
			t.Fatal(err)
		}
		results := make(chan error, 2)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); _, err := a.Approve(ctx, task.ApprovalID, "op-a", task.Args); results <- err }()
		go func() { defer wg.Done(); _, err := b.Approve(ctx, task.ApprovalID, "op-b", task.Args); results <- err }()
		wg.Wait()
		close(results)
		var succeeded, refused int
		for err := range results {
			if err == nil {
				succeeded++
			} else {
				refused++
			}
		}
		if succeeded != 1 || refused != 1 {
			t.Fatalf("approve race: %d won, %d refused; want exactly one winner", succeeded, refused)
		}
		finished, err := a.Get(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if finished.Status != StatusQueued || !approvalConsumed(t, a, task.ApprovalID) {
			t.Fatalf("task = %+v, want queued with a consumed approval", finished)
		}
		if countApprovals(t, a) != 1 {
			t.Fatal("approve race leaked approvals")
		}
	})

	t.Run("requeue", func(t *testing.T) {
		a, b := openPair(t)
		ctx := context.Background()
		task := submitWrite(t, a, "t1", "k-requeue")
		approveTask(t, a, task, "op")
		worker := &Worker{Store: a, Owner: "w1", Executor: okExecutor("x")}
		a.Hooks.BeforeStore = func(*Task) error { return errors.New("crash") }
		if _, err := worker.RunOnce(ctx); err == nil {
			t.Fatal("crash swallowed")
		}
		a.Hooks.BeforeStore = nil
		expireLease(t, a, task.ID)
		if _, err := a.Recover(ctx); err != nil {
			t.Fatal(err)
		}
		results := make(chan error, 2)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); _, err := a.RequeueUnknown(ctx, task.ID); results <- err }()
		go func() { defer wg.Done(); _, err := b.RequeueUnknown(ctx, task.ID); results <- err }()
		wg.Wait()
		close(results)
		var succeeded, refused int
		for err := range results {
			if err == nil {
				succeeded++
			} else {
				refused++
			}
		}
		if succeeded != 1 || refused != 1 {
			t.Fatalf("requeue race: %d won, %d refused; want exactly one winner", succeeded, refused)
		}
		// Original consumed approval plus exactly one fresh one: no orphan.
		if countApprovals(t, a) != 2 {
			t.Fatalf("requeue race leaked approvals")
		}
		moved, err := a.Get(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if moved.Status != StatusWaiting || moved.ApprovalID == task.ApprovalID {
			t.Fatalf("requeued = %+v, want waiting with a fresh approval", moved)
		}
	})

	t.Run("cancel beats approve", func(t *testing.T) {
		store := openSQLite(t)
		ctx := context.Background()
		task := submitWrite(t, store, "t1", "k-cancel")
		if _, err := store.Cancel(ctx, task.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Approve(ctx, task.ApprovalID, "op", task.Args); err == nil {
			t.Fatal("approve after cancel succeeded")
		}
		if approvalConsumed(t, store, task.ApprovalID) {
			t.Fatal("cancelled task consumed its approval: approval stranded")
		}
		canceled, err := store.Get(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if canceled.Status != StatusCanceled {
			t.Fatalf("task = %s, want canceled", canceled.Status)
		}
	})
}

// TestRenewLeaseExtendsAndFences proves renewal extends only the caller's
// own running task: strangers and terminal states get false, never an error
// that could be mistaken for ownership.
func TestRenewLeaseExtendsAndFences(t *testing.T) {
	store := openSQLite(t)
	ctx := context.Background()
	submitReadonly(t, store, "t1", "k1")
	claimed, err := store.Claim(ctx, "w1", 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.Get(ctx, claimed.ID)
	if err != nil {
		t.Fatal(err)
	}
	renewed, err := store.RenewLease(ctx, claimed.ID, "w1", 100*time.Second)
	if err != nil || !renewed {
		t.Fatalf("renew = %v, %v; want true, nil", renewed, err)
	}
	after, err := store.Get(ctx, claimed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.LeaseExpires <= before.LeaseExpires {
		t.Fatalf("lease %d not extended past %d", after.LeaseExpires, before.LeaseExpires)
	}
	if after.Version != before.Version {
		t.Fatalf("renewal bumped version %d -> %d; fencing chain owns versions", before.Version, after.Version)
	}
	if renewed, err := store.RenewLease(ctx, claimed.ID, "w2", time.Minute); err != nil || renewed {
		t.Fatalf("stranger renew = %v, %v; want false, nil", renewed, err)
	}
	if _, err := store.Complete(ctx, claimed.ID, claimed.Version, Outcome{Result: "x"}); err != nil {
		t.Fatal(err)
	}
	if renewed, err := store.RenewLease(ctx, claimed.ID, "w1", time.Minute); err != nil || renewed {
		t.Fatalf("terminal renew = %v, %v; want false, nil", renewed, err)
	}
}

// TestLongRunSurvivesLease is the heartbeat acceptance: an execution far
// longer than the lease still completes exactly once instead of being
// reaped mid-flight.
func TestLongRunSurvivesLease(t *testing.T) {
	store := openSQLite(t)
	ctx := context.Background()
	submitReadonly(t, store, "t1", "k1")
	var executions int
	worker := &Worker{Store: store, Owner: "w1", Lease: 60 * time.Millisecond, Executor: func(context.Context, *Task) Outcome {
		executions++
		time.Sleep(400 * time.Millisecond)
		return Outcome{Result: "slow"}
	}}
	finished, err := worker.RunOnce(ctx)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if finished.Status != StatusSucceeded || executions != 1 {
		t.Fatalf("finished = %+v executions = %d, want one success", finished, executions)
	}
	moved, err := store.Recover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if moved != 0 {
		t.Fatalf("recover moved %d, want 0: heartbeated task must not look crashed", moved)
	}
}

// TestReservationRefusesOverBudget ensures the budget gate fires before any
// side effect, not after.
func TestReservationRefusesOverBudget(t *testing.T) {
	store := openSQLite(t)
	ctx := context.Background()
	task, err := store.Submit(ctx, Task{TenantID: "t1", Owner: "u", IdempotencyKey: "poor", Kind: "write", Tool: "x", Args: `{}`, BudgetTokens: 50})
	if err != nil {
		t.Fatal(err)
	}
	approveTask(t, store, task, "op")
	called := false
	worker := &Worker{Store: store, Owner: "w1", WriteReserveTokens: 200, Executor: func(context.Context, *Task) Outcome {
		called = true
		return Outcome{Result: "x"}
	}}
	finished, err := worker.RunOnce(ctx)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if finished.Status != StatusFailed || called {
		t.Fatalf("over-budget write = %+v called=%v, want failed without execution", finished, called)
	}
}

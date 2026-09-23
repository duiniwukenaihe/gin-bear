// Package tasks adds durable work to the agent module: submit once,
// approve writes explicitly, execute under lease, and never retry blindly.
//
// States: queued, running, waiting_approval, succeeded, failed, canceled,
// timed_out, and unknown (external result unknown: reconcile, don't retry).
// Tenant plus idempotency key is unique; worker leases carry fencing
// versions; restart recovery preserves remaining budget instead of
// re-issuing it.
package tasks

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Statuses.
const (
	StatusQueued    = "queued"
	StatusRunning   = "running"
	StatusWaiting   = "waiting_approval"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
	StatusCanceled  = "canceled"
	StatusTimedOut  = "timed_out"
	StatusUnknown   = "unknown"
)

// ErrUnknownResult marks executions whose external effect is unknowable.
// The task moves to unknown: reconcile or escalate, never blind-retry.
var ErrUnknownResult = errors.New("external result unknown")

// Task is one durable unit of work.
type Task struct {
	ID             string
	TenantID       string
	Owner          string
	IdempotencyKey string
	Kind           string // "readonly" or "write"
	Tool           string
	Args           string
	ArgsHash       string
	PolicyVersion  string
	Status         string
	Version        int64
	ExecNonce      string
	ReservedTokens int64
	LeaseOwner     string
	LeaseExpires   int64
	BudgetTokens   int64
	ApprovalID     string
	Result         string
	ErrorText      string
	Attempts       int
	MaxAttempts    int
	ExpiresAt      int64
	CreatedAt      int64
	UpdatedAt      int64
}

// Approval binds one write execution: operator, tenant, tool, normalized
// args hash, policy version, expiry, single use. Parameter changes need a new
// approval because the hash stops matching.
type Approval struct {
	ID            string
	TenantID      string
	Operator      string
	Tool          string
	ArgsHash      string
	PolicyVersion string
	ExpiresAt     int64
	Consumed      bool
	ConsumedBy    string
	CreatedAt     int64
}

// Outcome is what one execution attempt reports.
type Outcome struct {
	Result    string
	Usage     int64
	Retryable bool
	Err       error
}

// Hooks inject crashes at commit boundaries for recovery tests.
type Hooks struct {
	BeforeClaim          func(*Task) error
	BeforeExecute        func(*Task) error
	BeforeStore          func(*Task) error
	BeforeApprovalCommit func(*Task) error
}

// Store persists tasks and approvals on the application's own database.
// Dialects: sqlite, postgres.
type Store struct {
	db      *sql.DB
	dialect string
	mu      sync.Mutex

	// PolicyVersion is the current policy generation. Write executions
	// require their approval's version to match; revocation bumps it.
	PolicyVersion string

	Hooks Hooks
}

// MigrationUp is the reviewed schema for the dialect. Down drops it.
// Schema changes are a deploy step; nothing here runs them implicitly.
func MigrationUp(dialect string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(dialect)) {
	case "sqlite", "postgres", "postgresql":
		return `CREATE TABLE IF NOT EXISTS agent_tasks (
  id TEXT PRIMARY KEY,
  tenant_id TEXT NOT NULL,
  owner TEXT NOT NULL,
  idempotency_key TEXT NOT NULL,
  kind TEXT NOT NULL,
  tool TEXT NOT NULL,
  args TEXT NOT NULL,
  args_hash TEXT NOT NULL,
  policy_version TEXT NOT NULL,
  status TEXT NOT NULL,
  version INTEGER NOT NULL DEFAULT 0,
  exec_nonce TEXT NOT NULL DEFAULT '',
  reserved_tokens INTEGER NOT NULL DEFAULT 0,
  lease_owner TEXT NOT NULL DEFAULT '',
  lease_expires INTEGER NOT NULL DEFAULT 0,
  budget_tokens INTEGER NOT NULL DEFAULT 0,
  approval_id TEXT NOT NULL DEFAULT '',
  result TEXT NOT NULL DEFAULT '',
  error_text TEXT NOT NULL DEFAULT '',
  attempts INTEGER NOT NULL DEFAULT 0,
  max_attempts INTEGER NOT NULL DEFAULT 3,
  expires_at INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  UNIQUE (tenant_id, idempotency_key)
);
CREATE TABLE IF NOT EXISTS agent_approvals (
  id TEXT PRIMARY KEY,
  tenant_id TEXT NOT NULL,
  operator TEXT NOT NULL,
  tool TEXT NOT NULL,
  args_hash TEXT NOT NULL,
  policy_version TEXT NOT NULL,
  expires_at INTEGER NOT NULL,
  consumed INTEGER NOT NULL DEFAULT 0,
  consumed_by TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS agent_task_audit (
  id TEXT PRIMARY KEY,
  task_id TEXT NOT NULL,
  tenant_id TEXT NOT NULL,
  actor TEXT NOT NULL,
  action TEXT NOT NULL,
  to_status TEXT NOT NULL,
  approval_id TEXT NOT NULL,
  usage INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_agent_task_audit_task ON agent_task_audit (task_id, created_at);`, nil
	default:
		return "", fmt.Errorf("unsupported tasks dialect %q", dialect)
	}
}

// MigrationDown drops the task schema. Never run implicitly.
func MigrationDown(dialect string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(dialect)) {
	case "sqlite", "postgres", "postgresql":
		return `DROP TABLE IF EXISTS agent_task_audit;
DROP TABLE IF EXISTS agent_approvals;
DROP TABLE IF EXISTS agent_tasks;`, nil
	default:
		return "", fmt.Errorf("unsupported tasks dialect %q", dialect)
	}
}

// Open creates a store. The caller applies MigrationUp explicitly first.
func Open(db *sql.DB, dialect string) (*Store, error) {
	normalized := strings.ToLower(strings.TrimSpace(dialect))
	if normalized != "sqlite" && normalized != "postgres" && normalized != "postgresql" {
		return nil, fmt.Errorf("unsupported tasks dialect %q", dialect)
	}
	return &Store{db: db, dialect: normalized}, nil
}

// Note for SQLite deployments: writers sharing one file across connections
// should set a busy timeout (e.g. ?_pragma=busy_timeout(5000)), otherwise
// contended writers report SQLITE_BUSY instead of blocking. Transactions
// retry those transient conflicts, but single-statement reads outside a
// transaction surface them to the caller.

// ArgsHash normalizes JSON args to a hex digest for approval binding.
func ArgsHash(args string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(args)))
	return hex.EncodeToString(sum[:])
}

func newID() string {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		panic(err)
	}
	return hex.EncodeToString(raw)
}

func now() int64 { return time.Now().Unix() }

// Submit queues work idempotently: same tenant plus key returns the existing
// task without creating a duplicate.
func (s *Store) Submit(ctx context.Context, task Task) (*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(task.TenantID) == "" || strings.TrimSpace(task.IdempotencyKey) == "" {
		return nil, fmt.Errorf("tenant and idempotency key are required")
	}
	if task.Kind != "readonly" && task.Kind != "write" {
		return nil, fmt.Errorf("task kind must be readonly or write")
	}
	if task.MaxAttempts <= 0 {
		task.MaxAttempts = 3
	}
	existing, err := s.getByTenantKey(ctx, task.TenantID, task.IdempotencyKey)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	task.ID = newID()
	task.ArgsHash = ArgsHash(task.Args)
	task.Status = StatusQueued
	task.PolicyVersion = s.PolicyVersion
	now := now()
	task.CreatedAt, task.UpdatedAt = now, now
	if task.Kind == "write" {
		// Approval and task insert commit together: a failed insert must
		// never leave an orphan approval, and a lost idempotency race must
		// roll back this attempt's approval before returning the winner.
		err := s.transact(ctx, func(tx *sql.Tx) error {
			approval := newApproval(s.PolicyVersion, task)
			if err := insertApprovalOn(s, tx, ctx, approval); err != nil {
				return fmt.Errorf("create approval: %w", err)
			}
			task.ApprovalID = approval.ID
			task.Status = StatusWaiting
			if err := insertTaskOn(s, tx, ctx, &task); err != nil {
				return err
			}
			return s.insertTaskAuditOn(tx, ctx, &task, "submit", task.Owner, 0)
		})
		if err != nil {
			// Lost race with a concurrent submit: this attempt rolled
			// back, so return the winner with no orphan left behind.
			if isUniqueViolation(err) {
				if existing, err2 := s.getByTenantKey(ctx, task.TenantID, task.IdempotencyKey); err2 == nil {
					return existing, nil
				}
			}
			return nil, err
		}
	} else if err := s.insert(ctx, &task); err != nil {
		// Lost race with a concurrent submit: return the winner.
		if existing, err2 := s.getByTenantKey(ctx, task.TenantID, task.IdempotencyKey); err2 == nil {
			return existing, nil
		}
		return nil, err
	}
	stored, err := s.get(ctx, task.ID)
	if err != nil {
		return nil, err
	}
	return stored, nil
}

// insertTaskOn is the transactional variant of insert; unique violations
// propagate unwrapped so callers can distinguish a lost race.
func insertTaskOn(s *Store, tx *sql.Tx, ctx context.Context, task *Task) error {
	_, err := s.execOn(tx, ctx, insertTaskSQL, insertTaskArgs(task)...)
	return err
}

// newApproval builds the pending approval for a write submit; operators
// Approve. Insertion happens inside the submit transaction.
func newApproval(policyVersion string, task Task) *Approval {
	return &Approval{
		ID:            "apr_" + newID(),
		TenantID:      task.TenantID,
		Operator:      "",
		Tool:          task.Tool,
		ArgsHash:      task.ArgsHash,
		PolicyVersion: policyVersion,
		ExpiresAt:     now() + 3600,
		CreatedAt:     now(),
	}
}

const insertApprovalSQL = `INSERT INTO agent_approvals
(id, tenant_id, operator, tool, args_hash, policy_version, expires_at, consumed, consumed_by, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, 0, '', ?)`

func insertApprovalOn(s *Store, tx *sql.Tx, ctx context.Context, approval *Approval) error {
	_, err := s.execOn(tx, ctx, insertApprovalSQL,
		approval.ID, approval.TenantID, approval.Operator, approval.Tool,
		approval.ArgsHash, approval.PolicyVersion, approval.ExpiresAt, approval.CreatedAt)
	return err
}

// insertTaskAuditOn records a write-task transition in the same transaction
// as the state change. It deliberately stores neither raw arguments nor the
// result. A failed audit insert rolls the transition back.
func (s *Store) insertTaskAuditOn(tx *sql.Tx, ctx context.Context, task *Task, action, actor string, usage int64) error {
	_, err := s.execOn(tx, ctx, `INSERT INTO agent_task_audit
(id, task_id, tenant_id, actor, action, to_status, approval_id, usage, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		newID(), task.ID, task.TenantID, actor, action, task.Status, task.ApprovalID, usage, now())
	return err
}

// Approve consumes one approval exactly once: expiry, tenant, args hash, and
// policy version must all match, and the task moves to queued. Parameter
// changes need a fresh approval because the hash stops matching. Consuming
// the approval and moving the task commit in one transaction: a crash or a
// concurrent cancel between them can neither strand a consumed approval on a
// waiting task nor overwrite the cancellation.
func (s *Store) Approve(ctx context.Context, approvalID, operator, args string) (*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(operator) == "" {
		return nil, fmt.Errorf("operator is required")
	}
	var approval Approval
	var consumed int
	err := s.queryRow(ctx, `SELECT id, tenant_id, operator, tool, args_hash, policy_version, expires_at, consumed, consumed_by, created_at
FROM agent_approvals WHERE id = ?`, approvalID).Scan(
		&approval.ID, &approval.TenantID, &approval.Operator, &approval.Tool,
		&approval.ArgsHash, &approval.PolicyVersion, &approval.ExpiresAt,
		&consumed, &approval.ConsumedBy, &approval.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("approval not found")
	}
	if err != nil {
		return nil, err
	}
	approval.Consumed = consumed != 0
	if approval.Consumed {
		return nil, fmt.Errorf("approval already consumed")
	}
	if now() > approval.ExpiresAt {
		return nil, fmt.Errorf("approval expired")
	}
	if ArgsHash(args) != approval.ArgsHash {
		return nil, fmt.Errorf("parameters changed since approval")
	}
	if approval.PolicyVersion != s.PolicyVersion {
		return nil, fmt.Errorf("policy changed since approval")
	}
	err = s.transact(ctx, func(tx *sql.Tx) error {
		result, err := s.execOn(tx, ctx, `UPDATE agent_approvals SET consumed = 1, consumed_by = ?, operator = ?
WHERE id = ? AND consumed = 0`, operator, operator, approvalID)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected != 1 {
			return fmt.Errorf("approval already consumed")
		}
		var taskID string
		err = s.queryRowOn(tx, ctx, `SELECT id FROM agent_tasks WHERE approval_id = ?`, approvalID).Scan(&taskID)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("no task for approval")
		}
		if err != nil {
			return err
		}
		task, err := s.getOn(tx, ctx, taskID)
		if err != nil {
			return err
		}
		if task.Status != StatusWaiting {
			return fmt.Errorf("task is %s, not waiting for approval", task.Status)
		}
		if task.Owner == operator {
			return fmt.Errorf("task owner cannot approve their own write")
		}
		if s.Hooks.BeforeApprovalCommit != nil {
			// A crash here must roll back the consume above: the
			// approval stays usable instead of stranding the task.
			if err := s.Hooks.BeforeApprovalCommit(task); err != nil {
				return err
			}
		}
		task.Status = StatusQueued
		task.UpdatedAt = now()
		// A concurrent Cancel flips status first, so the conditional write
		// moves zero rows: the cancellation wins and this consume rolls
		// back, leaving the approval usable for inspection, not stranded.
		committed, err := s.updateWhereOn(tx, ctx, task, `id = ? AND status = ?`, taskID, StatusWaiting)
		if err != nil {
			return err
		}
		if !committed {
			return fmt.Errorf("task changed during approval")
		}
		return s.insertTaskAuditOn(tx, ctx, task, "approve", operator, 0)
	})
	if err != nil {
		return nil, err
	}
	var taskID string
	if err := s.queryRow(ctx, `SELECT id FROM agent_tasks WHERE approval_id = ?`, approvalID).Scan(&taskID); err != nil {
		return nil, err
	}
	return s.get(ctx, taskID)
}

// Claim leases the oldest queued, unexpired task. The claim is one atomic
// conditional UPDATE on (id, version, status): two workers racing for the
// same row produce exactly one winner (RowsAffected == 1); the loser retries
// with the next candidate. The process-local mutex never replaces this
// SQL-level guarantee across instances.
func (s *Store) Claim(ctx context.Context, owner string, lease time.Duration) (*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Hooks.BeforeClaim != nil {
		probe := &Task{}
		if err := s.Hooks.BeforeClaim(probe); err != nil {
			return nil, err
		}
	}
	excluded := []any{}
	for attempt := 0; attempt < 8; attempt++ {
		var task Task
		query := `SELECT id, tenant_id, owner, idempotency_key, kind, tool, args, args_hash,
policy_version, status, version, exec_nonce, reserved_tokens, lease_owner, lease_expires,
budget_tokens, approval_id, result, error_text, attempts, max_attempts, expires_at,
created_at, updated_at
FROM agent_tasks WHERE status = ? AND (expires_at = 0 OR expires_at > ?) AND (lease_expires <= ? OR lease_owner = '')`
		args := []any{StatusQueued, now(), now()}
		if len(excluded) > 0 {
			marks := make([]string, 0, len(excluded))
			for range excluded {
				marks = append(marks, "?")
			}
			query += ` AND id NOT IN (` + strings.Join(marks, ", ") + `)`
			args = append(args, excluded...)
		}
		query += ` ORDER BY created_at ASC LIMIT 1`
		err := s.queryRow(ctx, query, args...).Scan(taskColumns(&task)...)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, sql.ErrNoRows
		}
		if err != nil {
			return nil, err
		}
		if task.BudgetTokens < 0 {
			return nil, fmt.Errorf("task budget exhausted")
		}
		claimed, won, err := s.claimCAS(ctx, &task, owner, lease)
		if err != nil {
			return nil, err
		}
		if won {
			return claimed, nil
		}
		excluded = append(excluded, task.ID)
	}
	return nil, sql.ErrNoRows
}

// claimCAS flips one known row to running iff its version and status are
// untouched. It reports won=false when another worker won the race.
func (s *Store) claimCAS(ctx context.Context, task *Task, owner string, lease time.Duration) (*Task, bool, error) {
	result, err := s.exec(ctx, `UPDATE agent_tasks SET status = ?, version = version + 1,
lease_owner = ?, lease_expires = ?, updated_at = ? WHERE id = ? AND version = ? AND status = ?`,
		StatusRunning, owner, time.Now().Add(lease).Unix(), now(), task.ID, task.Version, StatusQueued)
	if err != nil {
		return nil, false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, false, err
	}
	if affected != 1 {
		return nil, false, nil
	}
	claimed, err := s.get(ctx, task.ID)
	if err != nil {
		return nil, false, err
	}
	return claimed, true, nil
}

// Complete commits an attempt with a conditional write on (id, version,
// running). A concurrent Cancel or timeout changes status first, so the late
// Complete affects zero rows and the cancellation wins: the current row is
// returned and the stale result is dropped.
func (s *Store) Complete(ctx context.Context, id string, version int64, outcome Outcome) (*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if outcome.Usage < 0 {
		return nil, fmt.Errorf("usage cannot be negative")
	}
	if s.Hooks.BeforeStore != nil {
		if err := s.Hooks.BeforeStore(&Task{ID: id}); err != nil {
			return nil, err
		}
	}
	task, err := s.get(ctx, id)
	if err != nil {
		return nil, err
	}
	if task.Version != version {
		// A terminal state set by someone else (cancel, timeout) wins over
		// the late result: report it instead of overwriting it.
		if isTerminal(task.Status) {
			return task, nil
		}
		return nil, fmt.Errorf("fencing version mismatch: worker is stale")
	}
	if isTerminal(task.Status) {
		return task, nil
	}
	worker := task.LeaseOwner
	task.Attempts++
	if outcome.Err != nil {
		if errors.Is(outcome.Err, ErrUnknownResult) || (task.Kind == "write" && task.ExecNonce != "") {
			task.Status = StatusUnknown
			task.ErrorText = "external result unknown; reconcile, do not blind-retry"
		} else if outcome.Retryable && task.Attempts < task.MaxAttempts {
			task.Status = StatusQueued
			task.LeaseOwner, task.LeaseExpires = "", 0
			task.ErrorText = outcome.Err.Error()
		} else {
			task.Status = StatusFailed
			task.ErrorText = outcome.Err.Error()
		}
	} else {
		task.Status = StatusSucceeded
		task.Result = outcome.Result
	}
	// An unknown external result cannot be settled yet. Keep its reservation
	// until an operator confirms the actual usage or requeues with a new
	// approval; otherwise confirmation would charge it twice.
	if task.Status != StatusUnknown {
		if task.ReservedTokens > 0 {
			task.BudgetTokens += task.ReservedTokens - outcome.Usage
			task.ReservedTokens = 0
		} else {
			task.BudgetTokens -= outcome.Usage
		}
	}
	task.Version++
	task.UpdatedAt = now()
	committed := false
	err = s.transact(ctx, func(tx *sql.Tx) error {
		committed = false
		var err error
		committed, err = s.updateWhereOn(tx, ctx, task, `id = ? AND version = ? AND status = ?`, id, version, StatusRunning)
		if err != nil || !committed || task.Kind != "write" {
			return err
		}
		return s.insertTaskAuditOn(tx, ctx, task, "complete", worker, outcome.Usage)
	})
	if err != nil {
		return nil, err
	}
	if !committed {
		// Lost to Cancel/SweepTimeouts: report the winner's state instead of
		// overwriting it.
		return s.get(ctx, id)
	}
	return s.get(ctx, id)
}

// Cancel stops queued, waiting, or running work. It is an operator override:
// the conditional write wins over a late Complete, which then drops its
// result. Completed external effects are never undone; cancellation only
// prevents new side effects.
func (s *Store) Cancel(ctx context.Context, id string) (*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, err := s.get(ctx, id)
	if err != nil {
		return nil, err
	}
	if isTerminal(task.Status) {
		return task, nil
	}
	task.Status = StatusCanceled
	task.Version++
	task.LeaseOwner, task.LeaseExpires = "", 0
	task.UpdatedAt = now()
	committed, err := s.updateWhere(ctx, task, `id = ? AND status IN (?, ?, ?)`, id, StatusQueued, StatusWaiting, StatusRunning)
	if err != nil {
		return nil, err
	}
	if !committed {
		return s.get(ctx, id)
	}
	return s.get(ctx, id)
}

// SweepTimeouts marks expired non-terminal tasks timed out.
func (s *Store) SweepTimeouts(ctx context.Context) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.exec(ctx, `UPDATE agent_tasks SET status = ?, version = version + 1, updated_at = ?
WHERE status IN (?, ?, ?) AND expires_at > 0 AND expires_at <= ?`,
		StatusTimedOut, now(), StatusQueued, StatusRunning, StatusWaiting, now())
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// Recover requeues running tasks whose lease expired (crashed workers).
// Any write that recorded an execution intent (ExecNonce set before the
// external side effect, with or without a budget reservation) moves to
// unknown for reconciliation instead of being blind-retried; the nonce and
// any reservation are preserved. Writes that never reached intent (crashed
// before execution) and readonly work requeue with remaining budget intact,
// never re-issued.
func (s *Store) Recover(ctx context.Context) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	readonly, err := s.exec(ctx, `UPDATE agent_tasks SET status = ?, lease_owner = '', lease_expires = 0, version = version + 1, updated_at = ?
WHERE status = ? AND lease_expires <= ? AND NOT (kind = ? AND exec_nonce != '')`,
		StatusQueued, now(), StatusRunning, now(), "write")
	if err != nil {
		return 0, err
	}
	unknown, err := s.exec(ctx, `UPDATE agent_tasks SET status = ?, lease_owner = '', lease_expires = 0, version = version + 1, updated_at = ?
WHERE status = ? AND lease_expires <= ? AND kind = ? AND exec_nonce != ''`,
		StatusUnknown, now(), StatusRunning, now(), "write")
	if err != nil {
		return 0, err
	}
	moved, err := readonly.RowsAffected()
	if err != nil {
		return 0, err
	}
	held, err := unknown.RowsAffected()
	if err != nil {
		return 0, err
	}
	return moved + held, nil
}

// Reserve persists the execution intent of a write before any external side
// effect: it moves amount from budget to reservation under the claim's
// fencing version and records a fresh idempotency nonce. The reservation
// survives crashes, so recovery can reconcile instead of blind-retrying.
func (s *Store) Reserve(ctx context.Context, id string, version int64, amount int64) (*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, err := s.get(ctx, id)
	if err != nil {
		return nil, err
	}
	if task.Version != version {
		return nil, fmt.Errorf("fencing version mismatch: worker is stale")
	}
	if task.Status != StatusRunning {
		return nil, fmt.Errorf("task is %s, not running", task.Status)
	}
	if task.Kind != "write" {
		return nil, fmt.Errorf("reservation applies to write tasks")
	}
	if task.ReservedTokens > 0 {
		return task, nil
	}
	if amount <= 0 {
		return nil, fmt.Errorf("reservation amount must be positive")
	}
	if task.BudgetTokens < amount {
		return nil, fmt.Errorf("insufficient budget for reservation")
	}
	task.BudgetTokens -= amount
	task.ReservedTokens += amount
	task.ExecNonce = newID()
	task.Version++
	task.UpdatedAt = now()
	committed, err := s.updateWhere(ctx, task, `id = ? AND version = ? AND status = ?`, id, version, StatusRunning)
	if err != nil {
		return nil, err
	}
	if !committed {
		return nil, fmt.Errorf("task changed during reservation")
	}
	return s.get(ctx, id)
}

// RenewLease extends the lease of a running task owned by owner. It never
// touches version or any other column: the worker's in-flight fencing chain
// (claim → intent → reserve → complete) keeps working while the heartbeat
// runs. Only the owner of a running task can renew; anything else reports
// false so the worker stops instead of running unleased.
func (s *Store) RenewLease(ctx context.Context, id, owner string, lease time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if lease <= 0 {
		lease = time.Minute
	}
	result, err := s.exec(ctx, `UPDATE agent_tasks SET lease_expires = ?, updated_at = ?
WHERE id = ? AND lease_owner = ? AND status = ?`,
		now()+int64(lease/time.Second), now(), id, owner, StatusRunning)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected == 1, nil
}

// RecordIntent persists the execution intent of a write before any external
// side effect: it records a fresh idempotency nonce under the claim's
// fencing version, without moving budget. Every write records intent even
// when the worker carries no budget reservation, so recovery can tell
// "may have executed" (nonce set → unknown, reconcile) from "never ran"
// (no nonce → safe requeue). Correctness never depends on the optional
// budget parameter.
func (s *Store) RecordIntent(ctx context.Context, id string, version int64) (*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, err := s.get(ctx, id)
	if err != nil {
		return nil, err
	}
	if task.Version != version {
		return nil, fmt.Errorf("fencing version mismatch: worker is stale")
	}
	if task.Status != StatusRunning {
		return nil, fmt.Errorf("task is %s, not running", task.Status)
	}
	if task.Kind != "write" {
		return nil, fmt.Errorf("execution intent applies to write tasks")
	}
	task.ExecNonce = newID()
	task.Version++
	task.UpdatedAt = now()
	committed, err := s.updateWhere(ctx, task, `id = ? AND version = ? AND status = ?`, id, version, StatusRunning)
	if err != nil {
		return nil, err
	}
	if !committed {
		return nil, fmt.Errorf("task changed during intent recording")
	}
	return s.get(ctx, id)
}

// ConfirmUnknown reconciles an unconfirmed write after out-of-band
// verification: the recorded result commits with exact accounting against
// the surviving reservation. The write is fenced on the unknown state and the
// version just read, so two instances reconciling the same task produce one
// winner and a lost update is impossible across instances.
func (s *Store) ConfirmUnknown(ctx context.Context, id, result string, actualUsage int64) (*Task, error) {
	return s.ConfirmUnknownAs(ctx, id, "system", result, actualUsage)
}

// ConfirmUnknownAs persists the reconciler's identity with the settlement.
func (s *Store) ConfirmUnknownAs(ctx context.Context, id, actor, result string, actualUsage int64) (*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if actualUsage < 0 {
		return nil, fmt.Errorf("usage cannot be negative")
	}
	if strings.TrimSpace(actor) == "" {
		return nil, fmt.Errorf("actor is required")
	}
	err := s.transact(ctx, func(tx *sql.Tx) error {
		task, err := s.getOn(tx, ctx, id)
		if err != nil {
			return err
		}
		if task.Status != StatusUnknown {
			return fmt.Errorf("task is %s, not unknown", task.Status)
		}
		version := task.Version
		task.Status = StatusSucceeded
		task.Result = result
		task.BudgetTokens += task.ReservedTokens - actualUsage
		task.ReservedTokens = 0
		task.Attempts++
		task.Version++
		task.UpdatedAt = now()
		committed, err := s.updateWhereOn(tx, ctx, task, `id = ? AND status = ? AND version = ?`, id, StatusUnknown, version)
		if err != nil {
			return err
		}
		if !committed {
			return fmt.Errorf("task changed during confirmation")
		}
		return s.insertTaskAuditOn(tx, ctx, task, "confirm-unknown", actor, actualUsage)
	})
	if err != nil {
		return nil, err
	}
	return s.get(ctx, id)
}

// RequeueUnknown returns an unconfirmed write to the approval gate: the
// reservation refunds to budget, the consumed approval stays consumed, and a
// fresh approval is required before it can run again. The fresh approval and
// the task move commit in one transaction guarded by the unknown state, so
// concurrent requeues produce one winner with no orphan approval, and a
// crash between them leaves the task unknown instead of half-moved.
func (s *Store) RequeueUnknown(ctx context.Context, id string) (*Task, error) {
	return s.RequeueUnknownAs(ctx, id, "system")
}

// RequeueUnknownAs persists the reconciler's identity with the new approval.
func (s *Store) RequeueUnknownAs(ctx context.Context, id, actor string) (*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(actor) == "" {
		return nil, fmt.Errorf("actor is required")
	}
	if err := s.requeueUnknownTx(ctx, id, actor); err != nil {
		return nil, err
	}
	return s.get(ctx, id)
}

func (s *Store) requeueUnknownTx(ctx context.Context, id, actor string) error {
	return s.transact(ctx, func(tx *sql.Tx) error {
		task, err := s.getOn(tx, ctx, id)
		if err != nil {
			return err
		}
		if task.Status != StatusUnknown {
			return fmt.Errorf("task is %s, not unknown", task.Status)
		}
		version := task.Version
		task.BudgetTokens += task.ReservedTokens
		task.ReservedTokens = 0
		task.ExecNonce = ""
		task.Result, task.ErrorText = "", ""
		if task.Kind != "write" {
			task.Status = StatusQueued
		} else {
			approval := newApproval(s.PolicyVersion, *task)
			if err := insertApprovalOn(s, tx, ctx, approval); err != nil {
				return fmt.Errorf("create approval: %w", err)
			}
			task.ApprovalID = approval.ID
			task.Status = StatusWaiting
		}
		task.Version++
		task.UpdatedAt = now()
		committed, err := s.updateWhereOn(tx, ctx, task, `id = ? AND status = ? AND version = ?`, id, StatusUnknown, version)
		if err != nil {
			return err
		}
		if !committed {
			return fmt.Errorf("task changed during requeue")
		}
		return s.insertTaskAuditOn(tx, ctx, task, "requeue-unknown", actor, 0)
	})
}

// Get fetches one task.
func (s *Store) Get(ctx context.Context, id string) (*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.get(ctx, id)
}

func isTerminal(status string) bool {
	switch status {
	case StatusSucceeded, StatusFailed, StatusCanceled, StatusTimedOut, StatusUnknown:
		return true
	}
	return false
}

// Worker executes claimed tasks with one Executor. Client disconnect cancels
// the worker loop; persistent tasks use the service lifecycle context, so a
// disconnect never deletes them.
//
// WriteReserveTokens pre-reserves budget for write attempts before any
// external side effect. Zero skips the budget reservation only: the
// execution intent (idempotency nonce) is still persisted for every write,
// so crash recovery never blind-retries.
type Worker struct {
	Store    *Store
	Owner    string
	Lease    time.Duration
	Executor func(ctx context.Context, task *Task) Outcome
	// WriteAuthorize must check the current authoritative policy immediately
	// before a write attempt. A missing callback fails closed. The Store's
	// local PolicyVersion alone cannot observe revocation on another instance.
	WriteAuthorize     func(ctx context.Context, task *Task) error
	WriteReserveTokens int64
}

// RunOnce claims and executes a single task. sql.ErrNoRows means idle.
// A heartbeat renews the lease while the attempt runs, so long executions
// are not reaped mid-flight; it stops when RunOnce returns. Fencing still
// protects every state change, so a lost heartbeat can only delay, never
// duplicate, execution.
func (w *Worker) RunOnce(ctx context.Context) (*Task, error) {
	lease := w.Lease
	if lease <= 0 {
		lease = time.Minute
	}
	task, err := w.Store.Claim(ctx, w.Owner, lease)
	if err != nil {
		return nil, err
	}
	stopHeartbeat := w.startHeartbeat(ctx, task.ID)
	defer stopHeartbeat()
	version := task.Version
	if w.Store.Hooks.BeforeExecute != nil {
		// A crash before execution ran nothing: requeue, don't fail.
		if err := w.Store.Hooks.BeforeExecute(task); err != nil {
			return w.Store.Complete(ctx, task.ID, version, Outcome{Err: err, Retryable: true})
		}
	}
	if task.Kind == "write" {
		if err := w.checkWriteApproval(ctx, task); err != nil {
			return w.Store.Complete(ctx, task.ID, version, Outcome{Err: err})
		}
		if w.WriteAuthorize == nil {
			return w.Store.Complete(ctx, task.ID, version, Outcome{Err: fmt.Errorf("live write authorization is not configured")})
		}
		if err := w.WriteAuthorize(ctx, task); err != nil {
			return w.Store.Complete(ctx, task.ID, version, Outcome{Err: err})
		}
		if w.WriteReserveTokens > 0 {
			reserved, err := w.Store.Reserve(ctx, task.ID, version, w.WriteReserveTokens)
			if err != nil {
				return w.Store.Complete(ctx, task.ID, version, Outcome{Err: err})
			}
			task = reserved
			version = reserved.Version
		} else {
			recorded, err := w.Store.RecordIntent(ctx, task.ID, version)
			if err != nil {
				return w.Store.Complete(ctx, task.ID, version, Outcome{Err: err})
			}
			task = recorded
			version = recorded.Version
		}
	}
	outcome := w.Executor(ctx, task)
	return w.Store.Complete(ctx, task.ID, version, outcome)
}

// startHeartbeat renews the claim's lease every third of its duration until
// the returned stop function runs. Renewal never touches version, so the
// worker's fencing chain is unaffected; if renewal reports the task is gone,
// the heartbeat exits and the attempt's Complete still fences safely.
func (w *Worker) startHeartbeat(ctx context.Context, taskID string) context.CancelFunc {
	lease := w.Lease
	if lease <= 0 {
		lease = time.Minute
	}
	interval := lease / 3
	if interval < 10*time.Millisecond {
		interval = 10 * time.Millisecond
	}
	beatCtx, stop := context.WithCancel(ctx)
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-beatCtx.Done():
				return
			case <-ticker.C:
				renewed, err := w.Store.RenewLease(beatCtx, taskID, w.Owner, lease)
				if err != nil || !renewed {
					return
				}
			}
		}
	}()
	return stop
}

// checkWriteApproval re-verifies the approval at execution time: consumed,
// tenant, args hash, and current policy generation must all still hold.
func (w *Worker) checkWriteApproval(ctx context.Context, task *Task) error {
	if task.ApprovalID == "" {
		return fmt.Errorf("write task has no approval")
	}
	var consumed int
	var tenant, tool, argsHash, policyVersion string
	var expires int64
	err := w.Store.queryRow(ctx, `SELECT tenant_id, tool, args_hash, policy_version, expires_at, consumed
FROM agent_approvals WHERE id = ?`, task.ApprovalID).Scan(&tenant, &tool, &argsHash, &policyVersion, &expires, &consumed)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("approval gone")
	}
	if err != nil {
		return err
	}
	if consumed == 0 || tenant != task.TenantID || tool != task.Tool || argsHash != task.ArgsHash {
		return fmt.Errorf("approval does not cover this execution")
	}
	if policyVersion != w.Store.PolicyVersion {
		return fmt.Errorf("policy changed since approval")
	}
	if now() > expires {
		return fmt.Errorf("approval expired")
	}
	return nil
}

func taskColumns(task *Task) []any {
	return []any{&task.ID, &task.TenantID, &task.Owner, &task.IdempotencyKey, &task.Kind,
		&task.Tool, &task.Args, &task.ArgsHash, &task.PolicyVersion, &task.Status,
		&task.Version, &task.ExecNonce, &task.ReservedTokens, &task.LeaseOwner,
		&task.LeaseExpires, &task.BudgetTokens,
		&task.ApprovalID, &task.Result, &task.ErrorText, &task.Attempts,
		&task.MaxAttempts, &task.ExpiresAt, &task.CreatedAt, &task.UpdatedAt}
}

func (s *Store) insert(ctx context.Context, task *Task) error {
	_, err := s.exec(ctx, insertTaskSQL, insertTaskArgs(task)...)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("duplicate task: %w", err)
		}
		return err
	}
	return nil
}

// insertTaskSQL and insertTaskArgs are shared by the direct insert and the
// transactional submit path so both write identical rows.
const insertTaskSQL = `INSERT INTO agent_tasks
(id, tenant_id, owner, idempotency_key, kind, tool, args, args_hash, policy_version, status, version,
exec_nonce, reserved_tokens, lease_owner, lease_expires, budget_tokens, approval_id, result,
error_text, attempts, max_attempts, expires_at, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

func insertTaskArgs(task *Task) []any {
	return []any{
		task.ID, task.TenantID, task.Owner, task.IdempotencyKey, task.Kind, task.Tool,
		task.Args, task.ArgsHash, task.PolicyVersion, task.Status, task.Version,
		task.ExecNonce, task.ReservedTokens, task.LeaseOwner, task.LeaseExpires,
		task.BudgetTokens, task.ApprovalID,
		task.Result, task.ErrorText, task.Attempts, task.MaxAttempts,
		task.ExpiresAt, task.CreatedAt, task.UpdatedAt,
	}
}

// updateTaskColumns is the full-row SET list shared by the direct and
// transactional writers; the conditional suffix varies per fencing rule.
const updateTaskColumns = `tenant_id = ?, owner = ?, idempotency_key = ?,
kind = ?, tool = ?, args = ?, args_hash = ?, policy_version = ?, status = ?, version = ?,
exec_nonce = ?, reserved_tokens = ?, lease_owner = ?, lease_expires = ?, budget_tokens = ?,
approval_id = ?, result = ?, error_text = ?, attempts = ?, max_attempts = ?, expires_at = ?,
updated_at = ?`

func updateTaskArgs(task *Task) []any {
	return []any{
		task.TenantID, task.Owner, task.IdempotencyKey, task.Kind, task.Tool,
		task.Args, task.ArgsHash, task.PolicyVersion, task.Status, task.Version,
		task.ExecNonce, task.ReservedTokens, task.LeaseOwner, task.LeaseExpires,
		task.BudgetTokens, task.ApprovalID,
		task.Result, task.ErrorText, task.Attempts, task.MaxAttempts,
		task.ExpiresAt, task.UpdatedAt,
	}
}

// updateWhere writes the full row only when cond matches. It reports whether
// exactly one row moved, so racing writers observe a winner instead of
// silently overwriting each other.
func (s *Store) updateWhere(ctx context.Context, task *Task, cond string, args ...any) (bool, error) {
	result, err := s.exec(ctx, `UPDATE agent_tasks SET `+updateTaskColumns+` WHERE `+cond,
		append(updateTaskArgs(task), args...)...)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected == 1, nil
}

// updateWhereOn is the transactional variant of updateWhere.
func (s *Store) updateWhereOn(conn dbConn, ctx context.Context, task *Task, cond string, args ...any) (bool, error) {
	result, err := s.execOn(conn, ctx, `UPDATE agent_tasks SET `+updateTaskColumns+` WHERE `+cond,
		append(updateTaskArgs(task), args...)...)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected == 1, nil
}

const selectTaskSQL = `SELECT id, tenant_id, owner, idempotency_key, kind, tool, args, args_hash,
policy_version, status, version, exec_nonce, reserved_tokens, lease_owner, lease_expires,
budget_tokens, approval_id, result, error_text, attempts, max_attempts, expires_at,
created_at, updated_at
FROM agent_tasks WHERE id = ?`

func (s *Store) get(ctx context.Context, id string) (*Task, error) {
	return scanTask(s.queryRow(ctx, selectTaskSQL, id))
}

// getOn is the transactional variant of get.
func (s *Store) getOn(conn dbConn, ctx context.Context, id string) (*Task, error) {
	return scanTask(s.queryRowOn(conn, ctx, selectTaskSQL, id))
}

func scanTask(row *sql.Row) (*Task, error) {
	var task Task
	if err := row.Scan(taskColumns(&task)...); err != nil {
		return nil, err
	}
	return &task, nil
}

func (s *Store) getByTenantKey(ctx context.Context, tenant, key string) (*Task, error) {
	var id string
	err := s.queryRow(ctx, `SELECT id FROM agent_tasks WHERE tenant_id = ? AND idempotency_key = ?`, tenant, key).Scan(&id)
	if err != nil {
		return nil, err
	}
	return s.get(ctx, id)
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "UNIQUE constraint") || strings.Contains(message, "Duplicate entry") || strings.Contains(message, "duplicate key")
}

// exec and queryRow run static queries, rebinding ? placeholders to $n on
// PostgreSQL. SQLite uses ? natively. All task SQL avoids ? inside string
// literals so the rewrite is exact.
func (s *Store) exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return s.execOn(s.db, ctx, query, args...)
}

func (s *Store) queryRow(ctx context.Context, query string, args ...any) *sql.Row {
	return s.queryRowOn(s.db, ctx, query, args...)
}

// dbConn is satisfied by *sql.DB and *sql.Tx, so multi-statement state
// changes run inside one transaction instead of racing as separate commits.
type dbConn interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *Store) execOn(conn dbConn, ctx context.Context, query string, args ...any) (sql.Result, error) {
	return conn.ExecContext(ctx, s.rebind(query), args...)
}

func (s *Store) queryRowOn(conn dbConn, ctx context.Context, query string, args ...any) *sql.Row {
	return conn.QueryRowContext(ctx, s.rebind(query), args...)
}

// transact runs fn inside one database transaction, rolling back on any
// error. Cross-instance fencing still comes from conditional writes checked
// inside fn; the transaction only guarantees the grouped statements commit
// or roll back together.
//
// Lock-contention failures (SQLite SQLITE_BUSY instead of blocking,
// deadlocks) retry with backoff: fn must therefore be idempotent across
// attempts — every state change stays conditional and fresh IDs regenerate
// per attempt, so a replay either commits once or observes the winner.
func (s *Store) transact(ctx context.Context, fn func(tx *sql.Tx) error) error {
	const maxAttempts = 10
	wait := time.Millisecond
	for attempt := 1; ; attempt++ {
		err := s.transactOnce(ctx, fn)
		if err == nil {
			return nil
		}
		if !isLockContention(err) || attempt >= maxAttempts {
			return err
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		wait *= 2
	}
}

func (s *Store) transactOnce(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// isLockContention reports transient writer conflicts worth retrying: a busy
// SQLite file, a PostgreSQL deadlock, or a lock that could not be obtained.
// Unique violations and fencing mismatches are final and never retry.
func isLockContention(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	for _, marker := range []string{
		"SQLITE_BUSY", "database is locked",
		"deadlock detected", "could not obtain lock",
		"SQLSTATE 40P01", "SQLSTATE 55P03",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func (s *Store) rebind(query string) string {
	if s.dialect != "postgres" && s.dialect != "postgresql" {
		return query
	}
	var out strings.Builder
	numbered := 0
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			numbered++
			out.WriteString(fmt.Sprintf("$%d", numbered))
			continue
		}
		out.WriteByte(query[i])
	}
	return out.String()
}

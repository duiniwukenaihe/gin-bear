package agent

import (
	"sync"
	"sync/atomic"
	"time"
)

// AuditRecord is one authorization/tool/outcome fact. Debug logs are not
// audit logs: records are structured, bounded, and kept under a retention
// rule. Prompts and tool outputs are never stored, only digests.
type AuditRecord struct {
	At         time.Time `json:"at"`
	Actor      string    `json:"actor"`
	Tenant     string    `json:"tenant,omitempty"`
	Decision   string    `json:"decision"`
	Tool       string    `json:"tool,omitempty"`
	ArgsDigest string    `json:"args_digest,omitempty"`
	Approval   string    `json:"approval,omitempty"`
	Outcome    string    `json:"outcome,omitempty"`
	Provider   string    `json:"provider,omitempty"`
}

// Auditor appends bounded audit records with retention.
type Auditor struct {
	mu       sync.Mutex
	records  []AuditRecord
	retained int
	dropped  int64
}

// NewAuditor keeps at most retained records; older ones drop with a counter.
func NewAuditor(retained int) *Auditor {
	if retained <= 0 {
		retained = 1000
	}
	return &Auditor{retained: retained}
}

// Append records one fact. Actor/decision/tool come from the enforcement
// points, never from model text.
func (a *Auditor) Append(record AuditRecord) {
	if record.At.IsZero() {
		record.At = time.Now()
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.records = append(a.records, record)
	if len(a.records) > a.retained {
		over := len(a.records) - a.retained
		a.records = append([]AuditRecord(nil), a.records[over:]...)
		a.dropped += int64(over)
	}
}

// Snapshot returns a copy of retained records plus the drop count.
func (a *Auditor) Snapshot() ([]AuditRecord, int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]AuditRecord(nil), a.records...), a.dropped
}

// Metrics carries bounded counters only: tool/model/status categories.
// Run IDs, users, tenants, and prompts never become labels.
type Metrics struct {
	toolCalls   sync.Map // "tool\x00status" -> *atomic.Int64
	modelTurns  sync.Map // provider -> *atomic.Int64
	budgetEnds  atomic.Int64
	denials     atomic.Int64
	quotaBlocks atomic.Int64
}

func counter(m *sync.Map, key string) *atomic.Int64 {
	actual, _ := m.LoadOrStore(key, &atomic.Int64{})
	return actual.(*atomic.Int64)
}

// AddToolCall records one tool invocation outcome (ok, denied, error).
func (m *Metrics) AddToolCall(tool, status string) {
	counter(&m.toolCalls, tool+"\x00"+status).Add(1)
	if status == "denied" {
		m.denials.Add(1)
	}
}

// AddModelTurn records one model turn for a provider.
func (m *Metrics) AddModelTurn(provider string) {
	counter(&m.modelTurns, provider).Add(1)
}

// AddBudgetEnd records a budget-terminated run.
func (m *Metrics) AddBudgetEnd() { m.budgetEnds.Add(1) }

// AddQuotaBlock records an admission refusal.
func (m *Metrics) AddQuotaBlock() { m.quotaBlocks.Add(1) }

// AlertThresholds configures operator alerts over category counters.
type AlertThresholds struct {
	BudgetEnds  int64
	Denials     int64
	QuotaBlocks int64
}

// Alerts evaluates fixed operator conditions. Empty means quiet.
func (m *Metrics) Alerts(thresholds AlertThresholds) []string {
	var alerts []string
	if ends := m.budgetEnds.Load(); ends > thresholds.BudgetEnds {
		alerts = append(alerts, "agent budget exhaustion above threshold")
	}
	if denials := m.denials.Load(); denials > thresholds.Denials {
		alerts = append(alerts, "agent authorization denials above threshold")
	}
	if blocks := m.quotaBlocks.Load(); blocks > thresholds.QuotaBlocks {
		alerts = append(alerts, "agent quota blocks above threshold")
	}
	return alerts
}

// Snapshot returns category counters for operators.
func (m *Metrics) Snapshot() map[string]int64 {
	out := map[string]int64{
		"budget_ends":  m.budgetEnds.Load(),
		"denials":      m.denials.Load(),
		"quota_blocks": m.quotaBlocks.Load(),
	}
	m.toolCalls.Range(func(key, value any) bool {
		out["tool:"+key.(string)] = value.(*atomic.Int64).Load()
		return true
	})
	m.modelTurns.Range(func(key, value any) bool {
		out["model:"+key.(string)] = value.(*atomic.Int64).Load()
		return true
	})
	return out
}

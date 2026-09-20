package agent

import (
	"testing"
	"time"
)

func TestAuditorRetainsBoundedHistory(t *testing.T) {
	auditor := NewAuditor(3)
	for i := 0; i < 5; i++ {
		auditor.Append(AuditRecord{Actor: "u", Decision: "invoke:q", Outcome: "ok"})
	}
	records, dropped := auditor.Snapshot()
	if len(records) != 3 || dropped != 2 {
		t.Fatalf("records=%d dropped=%d, want 3/2", len(records), dropped)
	}
	if records[0].At.IsZero() {
		t.Fatal("audit record has no timestamp")
	}
	if time.Since(records[0].At) > time.Minute {
		t.Fatal("audit timestamp not stamped at append")
	}
}

func TestMetricsSnapshotAndAlerts(t *testing.T) {
	metrics := &Metrics{}
	if alerts := metrics.Alerts(AlertThresholds{}); len(alerts) != 0 {
		t.Fatalf("quiet metrics raised %v", alerts)
	}
	metrics.AddToolCall("get_order", "ok")
	metrics.AddToolCall("get_order", "denied")
	metrics.AddModelTurn("fake")
	metrics.AddBudgetEnd()
	metrics.AddQuotaBlock()
	snapshot := metrics.Snapshot()
	if snapshot["tool:get_order\x00ok"] != 1 || snapshot["tool:get_order\x00denied"] != 1 {
		t.Fatalf("tool counters = %v", snapshot)
	}
	if snapshot["model:fake"] != 1 || snapshot["budget_ends"] != 1 || snapshot["quota_blocks"] != 1 || snapshot["denials"] != 1 {
		t.Fatalf("snapshot = %v", snapshot)
	}
	alerts := metrics.Alerts(AlertThresholds{})
	if len(alerts) != 3 {
		t.Fatalf("alerts = %v, want all three conditions", alerts)
	}
	quiet := metrics.Alerts(AlertThresholds{BudgetEnds: 10, Denials: 10, QuotaBlocks: 10})
	if len(quiet) != 0 {
		t.Fatalf("raised alerts under high thresholds: %v", quiet)
	}
}

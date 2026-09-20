package example

import (
	"context"
	"strings"
	"testing"

	agent "github.com/duiniwukenaihe/gin-bear/extensions/agent"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func testStore(t *testing.T) (*Store, Order, Order) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	store, err := OpenStore(db)
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.Seed("tenant-a", "widget", 100)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Seed("tenant-b", "gadget", 200)
	if err != nil {
		t.Fatal(err)
	}
	return store, *first, *second
}

func callTool(t *testing.T, store *Store, identity agent.Identity, orderID string) (string, error) {
	t.Helper()
	tool := GetOrderTool(store)
	if err := tool.Authorize(context.Background(), identity, map[string]any{"order_id": orderID}); err != nil {
		return "", err
	}
	ctx := agent.WithIdentity(context.Background(), identity)
	return tool.Execute(ctx, map[string]any{"order_id": orderID})
}

// TestTenantIsolation is the W8 cross-tenant acceptance: the same id reads
// for its own tenant and reads as not-found for everyone else.
func TestTenantIsolation(t *testing.T) {
	store, first, second := testStore(t)
	alice := agent.Identity{UserID: "alice", TenantID: "tenant-a"}
	bob := agent.Identity{UserID: "bob", TenantID: "tenant-b"}

	payload, err := callTool(t, store, alice, uintString(first.ID))
	if err != nil {
		t.Fatalf("own order: %v", err)
	}
	if !strings.Contains(payload, "widget") {
		t.Fatalf("own order payload = %s", payload)
	}
	if _, err := callTool(t, store, alice, uintString(second.ID)); err == nil {
		t.Fatal("cross-tenant read succeeded")
	} else if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("cross-tenant error = %v, want not found (no existence leak)", err)
	}
	if _, err := callTool(t, store, bob, uintString(first.ID)); err == nil {
		t.Fatal("reverse cross-tenant read succeeded")
	}
	if _, err := callTool(t, store, agent.Identity{UserID: "ghost"}, uintString(first.ID)); err == nil {
		t.Fatal("tenant-less identity authorized")
	}
}

// TestOrderIDShape rejects malformed ids before any query.
func TestOrderIDShape(t *testing.T) {
	store, _, _ := testStore(t)
	for _, bad := range []string{"", "0", "-1", "abc", "1; DROP TABLE orders"} {
		if _, err := callTool(t, store, agent.Identity{UserID: "alice", TenantID: "tenant-a"}, bad); err == nil {
			t.Fatalf("order_id %q accepted", bad)
		}
	}
}

func uintString(id uint) string {
	if id == 0 {
		return "0"
	}
	digits := []byte{}
	for value := id; value > 0; value /= 10 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
	}
	return string(digits)
}

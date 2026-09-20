// Package example wires one tenant-scoped read-only business tool into a
// bear application. It is the reference integration: identity arrives from
// trusted middleware, route authorization reuses the framework
// PermissionFairing, and the tool constrains every query by tenant.
package example

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/cloudwego/eino/schema"
	agent "github.com/duiniwukenaihe/gin-bear/extensions/agent"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/duiniwukenaihe/gin-bear/pkg/bear"
)

// Order is the business resource. Tenant isolation is a database condition,
// not a model promise.
type Order struct {
	ID       uint   `gorm:"primaryKey" json:"id"`
	TenantID string `gorm:"index" json:"-"`
	Item     string `json:"item"`
	Amount   int64  `json:"amount"`
}

// Store owns the orders table.
type Store struct {
	DB *gorm.DB
}

// OpenStore migrates the orders table on an application-owned database.
func OpenStore(db *gorm.DB) (*Store, error) {
	if err := db.AutoMigrate(&Order{}); err != nil {
		return nil, err
	}
	return &Store{DB: db}, nil
}

// Seed creates one order for a tenant. Test/demo use only.
func (s *Store) Seed(tenant, item string, amount int64) (*Order, error) {
	order := &Order{TenantID: tenant, Item: item, Amount: amount}
	if err := s.DB.Create(order).Error; err != nil {
		return nil, err
	}
	return order, nil
}

// GetOrderTool returns the single read-only tool. Authorization requires a
// tenant-bearing identity; execution constrains the row by that tenant, so a
// cross-tenant id reads as not-found instead of leaking existence.
func GetOrderTool(store *Store) *agent.Tool {
	return &agent.Tool{
		Info: newToolInfo(),
		Authorize: func(ctx context.Context, identity agent.Identity, args map[string]any) error {
			if identity.UserID == "" || identity.TenantID == "" {
				return fmt.Errorf("tenant identity required")
			}
			return nil
		},
		Execute: func(ctx context.Context, args map[string]any) (string, error) {
			raw, _ := args["order_id"].(string)
			id, err := strconv.ParseUint(raw, 10, 64)
			if err != nil || id == 0 {
				return "", fmt.Errorf("invalid order_id")
			}
			identity, ok := agent.IdentityFromContext(ctx)
			if !ok {
				return "", fmt.Errorf("missing identity")
			}
			var order Order
			if err := store.DB.WithContext(ctx).Where("id = ? AND tenant_id = ?", id, identity.TenantID).First(&order).Error; err != nil {
				if err == gorm.ErrRecordNotFound {
					return "", fmt.Errorf("order not found")
				}
				return "", err
			}
			payload, err := json.Marshal(map[string]any{"id": order.ID, "item": order.Item, "amount": order.Amount})
			if err != nil {
				return "", err
			}
			return string(payload), nil
		},
	}
}

func newToolInfo() *schema.ToolInfo {
	return &schema.ToolInfo{
		Name: "get_order",
		Desc: "Read one order the caller is entitled to see. Arguments: {\"order_id\": \"<id>\"}. Never invent ids; only call with ids the user gave or a previous tool returned.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"order_id": {Type: schema.String, Desc: "order id", Required: true},
		}),
	}
}

// IdentityFairing is test/trusted middleware: it resolves the caller identity
// from the request context through Resolve. Production deployments resolve
// from authenticated session state, never from model or client text.
type IdentityFairing struct {
	bear.BaseFairing
	Resolve func(*gin.Context) (agent.Identity, error)
}

// Name identifies the fairing.
func (f *IdentityFairing) Name() string { return "AgentIdentity" }

// OnRequest stores trusted identity for downstream handlers and tools.
func (f *IdentityFairing) OnRequest(ctx *gin.Context) error {
	if f.Resolve == nil {
		return bear.ErrInternalServer
	}
	identity, err := f.Resolve(ctx)
	if err != nil || identity.UserID == "" {
		return bear.ErrUnauthorized
	}
	agent.SetIdentity(ctx, identity)
	return nil
}

// Module mounts the read-only agent routes under /agent. Route-level
// authorization stays with the application's own PermissionFairing.
type Module struct {
	Handler *agent.Handler
}

// Name identifies the module.
func (m *Module) Name() string { return "AgentExampleModule" }

// Beans exposes nothing; the handler is attached directly.
func (m *Module) Beans() []bear.Bean { return nil }

// Build mounts invoke (JSON) and stream (SSE) routes.
func (m *Module) Build(app *bear.Bear) {
	app.HandleE("POST", "/agent/invoke", m.Handler.Invoke)
	app.HandleE("GET", "/agent/stream", m.Handler.Stream)
}

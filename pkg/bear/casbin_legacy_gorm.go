//go:build !bear_no_casbin && !bear_casbin_no_gorm_adapter

package bear

import (
	"fmt"

	gormadapter "github.com/casbin/gorm-adapter/v3"
)

// NewCasbinEnforcer preserves the legacy GORM-based constructor.
func NewCasbinEnforcer(adapter *GormAdapter, cfg *CasbinConfig) (*CasbinEnforcer, error) {
	if adapter == nil {
		return NewCasbinEnforcerWithAdapter(nil, cfg)
	}
	store, err := gormadapter.NewAdapterByDB(adapter.DB)
	if err != nil {
		return nil, fmt.Errorf("failed to create Casbin adapter: %w", err)
	}
	return NewCasbinEnforcerWithAdapter(store, cfg)
}

// NewCasbinAuthorizer preserves the legacy GORM-based constructor.
func NewCasbinAuthorizer(adapter *GormAdapter, cfg *CasbinConfig) (*CasbinAuthorizer, error) {
	if adapter == nil {
		return NewCasbinAuthorizerWithAdapter(nil, cfg)
	}
	store, err := gormadapter.NewAdapterByDB(adapter.DB)
	if err != nil {
		return nil, fmt.Errorf("failed to create Casbin adapter: %w", err)
	}
	return NewCasbinAuthorizerWithAdapter(store, cfg)
}

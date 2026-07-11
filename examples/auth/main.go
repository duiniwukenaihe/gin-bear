// Package main demonstrates handling the typed token-revocation availability
// error without treating a missing Redis integration as a panic condition.
package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/duiniwukenaihe/gin-bear/pkg/bear"
)

func revokeToken(ctx context.Context, manager *bear.AuthTokenManager, token string) error {
	if err := manager.RevokeToken(ctx, token); err != nil {
		if errors.Is(err, bear.ErrTokenRevocationUnavailable) {
			return fmt.Errorf("token revocation requires a configured Redis client: %w", err)
		}
		return fmt.Errorf("revoke token: %w", err)
	}
	return nil
}

func main() {
	// Obtain the manager from the application runtime after dependency injection.
	_ = revokeToken
}

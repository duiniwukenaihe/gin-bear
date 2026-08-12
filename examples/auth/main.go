// Package main demonstrates handling the typed token-revocation availability
// error without treating a missing Redis integration as a panic condition.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/duiniwukenaihe/gin-bear/pkg/bear"
	"github.com/gin-gonic/gin"
)

type logoutRequest struct {
	Token string `json:"token" binding:"required"`
}

func revokeToken(ctx context.Context, manager *bear.AuthTokenManager, token string) error {
	if err := manager.RevokeToken(ctx, token); err != nil {
		if errors.Is(err, bear.ErrTokenRevocationUnavailable) {
			return fmt.Errorf("token revocation requires a configured Redis client: %w", err)
		}
		return fmt.Errorf("revoke token: %w", err)
	}
	return nil
}

func buildApp() (*bear.Bear, error) {
	config := bear.NewSysConfig()
	config.DB.Enabled = false
	config.SetFrameworkStrict(true)
	manager := bear.NewAuthTokenManager()
	app, err := bear.IgniteE(config)
	if err != nil {
		return nil, fmt.Errorf("initialize application: %w", err)
	}
	if err := app.BeansE(manager); err != nil {
		return nil, fmt.Errorf("register token manager: %w", err)
	}
	if _, err := app.POSTE("/logout", func(ctx *gin.Context) {
		var request logoutRequest
		if err := ctx.ShouldBindJSON(&request); err != nil {
			ctx.JSON(http.StatusBadRequest, gin.H{"error": "token is required"})
			return
		}
		if err := revokeToken(ctx.Request.Context(), manager, request.Token); err != nil {
			if errors.Is(err, bear.ErrTokenRevocationUnavailable) {
				ctx.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
				return
			}
			ctx.JSON(http.StatusInternalServerError, gin.H{"error": "token revocation failed"})
			return
		}
		ctx.Status(http.StatusNoContent)
	}); err != nil {
		return nil, fmt.Errorf("register logout route: %w", err)
	}
	return app, nil
}

func run(ctx context.Context) error {
	app, err := buildApp()
	if err != nil {
		return err
	}
	if err := app.Serve(ctx); err != nil {
		return fmt.Errorf("serve application: %w", err)
	}
	return nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// Package main is a runnable minimal HTTP service. Its startup sequence is
// verified by main_test.go and mirrored by the README.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/duiniwukenaihe/gin-bear/pkg/bear"
)

type greetingController struct{}

func (greetingController) Name() string { return "greetingController" }

func (greetingController) Build(app *bear.Bear) {
	app.Handle("GET", "/hello", func() string { return "hello from gin-bear" })
}

func newApp() *bear.Bear {
	config := bear.NewSysConfig()
	config.Server.Port = 8080
	return bear.Ignite(config).
		Mount("/api", greetingController{}).
		EnableHealth()
}

func run(ctx context.Context) error {
	app := newApp()
	if err := app.ApplyAll(ctx); err != nil {
		return fmt.Errorf("initialize application: %w", err)
	}
	if err := app.Launch(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("launch application: %w", err)
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

// Package main is a runnable minimal HTTP service. Its startup sequence is
// verified by main_test.go and mirrored by the README.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/duiniwukenaihe/gin-bear/pkg/bear"
)

type greetingController struct{}

func (greetingController) Name() string { return "greetingController" }

func (greetingController) Build(app *bear.Bear) {
	if err := (greetingController{}).BuildE(app); err != nil {
		panic(err)
	}
}

func (greetingController) BuildE(app *bear.Bear) error {
	return app.HandleE("GET", "/hello", func() string { return "hello from gin-bear" })
}

func buildApp() (*bear.Bear, error) {
	config := bear.NewSysConfig()
	config.Server.Port = 8080
	config.SetFrameworkStrict(true)
	app, err := bear.IgniteE(config)
	if err != nil {
		return nil, fmt.Errorf("initialize application: %w", err)
	}
	if err := app.MountE("/api", greetingController{}); err != nil {
		return nil, fmt.Errorf("register greeting controller: %w", err)
	}
	if err := app.EnableHealthE(); err != nil {
		return nil, fmt.Errorf("initialize health: %w", err)
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

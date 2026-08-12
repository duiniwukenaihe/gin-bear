package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/duiniwukenaihe/gin-bear/pkg/bear"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	app, err := bear.IgniteE()
	if err != nil {
		return fmt.Errorf("initialize application: %w", err)
	}
	if err := app.MountE("/api", &UserController{}); err != nil {
		return fmt.Errorf("register user controller: %w", err)
	}
	if err := app.EnableHealthE(); err != nil {
		return fmt.Errorf("initialize health: %w", err)
	}
	if err := app.Serve(ctx); err != nil {
		return fmt.Errorf("serve application: %w", err)
	}
	return nil
}

// --- 模型定义 ---

type User struct {
	ID   uint `gorm:"primaryKey"`
	Name string
	Age  int
}

// --- 控制器定义 ---

type UserController struct {
	bear.Repository[User]
}

func (c *UserController) Name() string { return "UserController" }

func (c *UserController) Build(b *bear.Bear) {
	if err := c.BuildE(b); err != nil {
		panic(err)
	}
}

func (c *UserController) BuildE(b *bear.Bear) error {
	if err := b.HandleE("GET", "/hello", c.Hello); err != nil {
		return err
	}
	if err := b.HandleE("GET", "/users", c.List); err != nil {
		return err
	}
	return b.HandleE("GET", "/users/:id", c.GetByID)
}

// Hello 打招呼
func (c *UserController) Hello() string {
	return "Hello, gin-bear!"
}

// List 用户列表
func (c *UserController) List() ([]*User, error) {
	return []*User{{ID: 1, Name: "Tom", Age: 20}}, nil
}

// GetByID 获取用户
func (c *UserController) GetByID(id int) (*User, error) {
	return &User{ID: uint(id), Name: "User" + fmt.Sprint(id)}, nil
}

package main

import (
	"bear/pkg/bear"
	"context"
	"fmt"
)

func main() {
	app := bear.Ignite()
	app.Mount("/api", &UserController{})
	app.EnableHealth()

	ctx := context.Background()
	if err := app.ApplyAll(ctx); err != nil {
		fmt.Println("ApplyAll error:", err)
		return
	}

	if err := app.Launch(ctx); err != nil {
		fmt.Println("Launch error:", err)
	}
}

// --- 模型定义 ---

type User struct {
	ID   uint   `gorm:"primaryKey"`
	Name string
	Age  int
}

// --- 控制器定义 ---

type UserController struct {
	bear.Repository[User]
}

func (c *UserController) Name() string { return "UserController" }

func (c *UserController) Build(b *bear.Bear) {
	b.Handle("GET", "/hello", c.Hello)
	b.Handle("GET", "/users", c.List)
	b.Handle("GET", "/users/:id", c.GetByID)
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

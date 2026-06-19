# 🐻 Gin-Bear

[English](#english) | [Chinese](#chinese)

---

## English

### Introduction

`gin-bear` is a modern Go Web framework based on Gin, pursuing minimalist architecture. It provides IoC container, GORM generics, Fairing interceptors, JWT authentication, and rate limiting.

### Core Features

| Feature | Description |
|---------|-------------|
| IoC Container | Dependency injection, auto-wiring |
| GORM Generics | Powerful ORM with type safety |
| Fairing System | Request/response interceptors |
| Module System | Organize beans and routes |
| JWT Auth | Built-in authentication |
| Rate Limiter | Memory or Redis-based |

### Quick Start

```bash
# Clone
git clone https://github.com/duiniwukenaihe/gin-bear.git
cd gin-bear

# Configure
# Edit application.yaml with your database credentials

# Run
go run cmd/main.go
```

### Example

```go
package main

import (
    "context"
    "github.com/duiniwukenaihe/gin-bear/pkg/bear"
)

func main() {
    app := bear.Ignite()
    app.Mount("/api", &UserController{})
    app.EnableHealth()

    ctx := context.Background()
    app.ApplyAll(ctx)
    app.Launch(ctx)
}
```

---

### Project Structure

```
.
├── cmd/
│   └── main.go              # Entry point
├── internal/                # Business logic
│   ├── controller/         # Controllers
│   ├── service/            # Services
│   ├── repository/         # Repositories
│   └── model/              # Models
├── pkg/
│   └── bear/               # Framework core
├── application.yaml         # Config file
└── locales/                # i18n files
```

---

### Config File

```yaml
server:
  port: 8080
  name: "my-app"
  shutdown_timeout: "10s"

health:
  readiness_timeout: "3s"

log:
  level: "info"

database:
  type: "mysql"
  host: "localhost"
  port: "3306"
  user: "root"
  password: "your-password"
  dbname: "myapp"

redis:
  addr: "localhost:6379"
  required: false

auth:
  jwt_secret: "replace-with-at-least-32-random-characters"
  token_expire_hours: 24
```

---

### Tutorial: User Service

**Step 1: Define Model**

```go
package model

import "time"

type User struct {
    ID        uint      `gorm:"primaryKey"`
    Username  string    `gorm:"uniqueIndex;size:50"`
    Email     string    `gorm:"size:100"`
    Password  string    `gorm:"size:255"`
    Status    int       `gorm:"default:1"`
    CreatedAt time.Time
    UpdatedAt time.Time
}
```

**Step 2: Define Repository**

```go
package repository

import (
    "my-app/internal/model"
    "github.com/duiniwukenaihe/gin-bear/pkg/bear"
)

type UserRepository struct {
    bear.Repository[model.User]
}

func (r *UserRepository) Name() string { return "UserRepository" }
```

**Step 3: Define Service**

```go
package service

import (
    "context"
    "my-app/internal/model"
    "my-app/internal/repository"
)

type UserService struct {
    Repo *repository.UserRepository `inject:"-"`
}

func (s *UserService) Name() string { return "UserService" }

func (s *UserService) CreateUser(ctx context.Context, user *model.User) error {
    return s.Repo.Create(ctx, user)
}
```

**Step 4: Define Controller**

```go
package controller

import (
    "my-app/internal/model"
    "my-app/internal/service"
    "github.com/duiniwukenaihe/gin-bear/pkg/bear"
)

type UserController struct {
    Svc *service.UserService `inject:"-"`
}

func (c *UserController) Name() string { return "UserController" }

func (c *UserController) Build(b *bear.Bear) {
    b.Handle("GET", "/users", c.List)
    b.Handle("GET", "/users/:id", c.GetByID)
    b.Handle("POST", "/users", c.Create)
}

type CreateUserReq struct {
    Username string `json:"username" binding:"required"`
    Email    string `json:"email" binding:"required,email"`
    Password string `json:"password" binding:"required,min=6"`
}

func (c *UserController) Create(req *CreateUserReq) (*model.User, error) {
    user := &model.User{
        Username: req.Username,
        Email:    req.Email,
        Password: req.Password,
    }
    return user, c.Svc.CreateUser(nil, user)
}
```

**Step 5: Assemble App**

```go
package main

import (
    "context"
    "github.com/duiniwukenaihe/gin-bear/pkg/bear"
)

func main() {
    app := bear.Ignite()
    app.Beans(&UserRepository{}, &UserService{})
    app.Mount("/api/v1", &UserController{})
    app.EnableHealth()

    ctx := context.Background()
    app.ApplyAll(ctx)
    app.Launch(ctx)
}
```

---

### Core Features

**Dependency Injection**

```go
type MyService struct {
    Repo *UserRepository `inject:"-"`
}

func (s *MyService) Name() string { return "MyService" }

// Get Bean
svc := bear.GetByType[*UserService]()
```

**Repository Generics**

```go
type UserRepository struct {
    bear.Repository[User]
}

// Built-in methods
repo.Create(ctx, user)
repo.FindByID(ctx, id)
repo.FindOne(ctx, cond)
repo.FindList(ctx, cond)
repo.Update(ctx, user)
repo.Delete(ctx, user)
repo.Count(ctx)
```

**Fairing Interceptor**

```go
type LoggingFairing struct {
    bear.BaseFairing
}

func (f *LoggingFairing) OnRequest(ctx *gin.Context) error {
    log.Info("Request started", "path", ctx.Request.URL.Path)
    return nil
}

app.Attach(&LoggingFairing{})
```

**JWT Authentication**

```go
jwtUtil := bear.NewJWTUtil(secret, expireHours)
token, err := jwtUtil.GenerateToken(userID, username)
app.Attach(bear.NewAuthFairing())
```

**Rate Limiter**

```go
limiter := bear.NewMemoryRateLimiter(100, time.Second)
app.Use(bear.RateLimitMiddleware(limiter))
```

---

### Controller Signatures

| Signature | Description |
|-----------|-------------|
| `func(*gin.Context)` | Standard Gin |
| `func(*gin.Context) (interface{}, error)` | With error handling |
| `func(*Req) (*Res, error)` | Auto binding |
| `func() string` | Return string |
| `func() interface{}` | Return JSON |

---

### Module System

```go
type UserModule struct{}

func (m *UserModule) Name() string { return "UserModule" }

func (m *UserModule) Beans() []bear.Bean {
    return []bear.Bean{&UserRepository{}, &UserService{}}
}

func (m *UserModule) Build(b *bear.Bear) {
    b.Mount("/api/v1/users", &UserController{})
}

app.AddModule(&UserModule{})
```

---

### FAQ

**Q: Controller method not executed?**

Make sure to implement `Name() string`:

```go
func (c *UserController) Name() string { return "UserController" }
```

**Q: Dependency injection failed?**

1. Check if `app.ApplyAll(ctx)` is called
2. Check if field has `` `inject:"-"` `` tag
3. Check if Bean is registered

**Q: Database connection failed?**

Make sure database is created:

```bash
mysql> CREATE DATABASE myapp;
psql> CREATE DATABASE myapp;
```

---

## Chinese

### 简介

`gin-bear` 是基于 Gin 框架的现代化 Go Web 开发脚手架，追求精简极致的架构设计。提供 IoC 容器、GORM 泛型、Fairing 拦截器、JWT 认证、接口限流等功能。

### 核心特性

| 特性 | 说明 |
|------|------|
| IoC 容器 | 依赖注入，自动装配 |
| GORM 泛型 | 强大的 ORM 支持 |
| Fairing 拦截器 | 请求/响应拦截 |
| 模块系统 | 按模块组织代码 |
| JWT 认证 | 内置认证鉴权 |
| 接口限流 | 内存/Redis 限流 |

### 快速开始

```bash
# 克隆
git clone https://github.com/duiniwukenaihe/gin-bear.git
cd gin-bear

# 配置
# 编辑 application.yaml 修改数据库连接信息

# 运行
go run cmd/main.go
```

### 示例

```go
package main

import (
    "context"
    "github.com/duiniwukenaihe/gin-bear/pkg/bear"
)

func main() {
    app := bear.Ignite()
    app.Mount("/api", &UserController{})
    app.EnableHealth()

    ctx := context.Background()
    app.ApplyAll(ctx)
    app.Launch(ctx)
}
```

---

### 项目结构

```
.
├── cmd/
│   └── main.go              # 程序入口
├── internal/                # 业务代码
│   ├── controller/         # 控制器层
│   ├── service/            # 业务逻辑层
│   ├── repository/         # 数据层
│   └── model/              # 数据模型
├── pkg/
│   └── bear/               # 框架核心
├── application.yaml         # 配置文件
└── locales/                # i18n 翻译文件
```

---

### 配置文件

```yaml
server:
  port: 8080
  name: "my-app"
  shutdown_timeout: "10s"

health:
  readiness_timeout: "3s"

log:
  level: "info"

database:
  type: "mysql"
  host: "localhost"
  port: "3306"
  user: "root"
  password: "your-password"
  dbname: "myapp"

redis:
  addr: "localhost:6379"
  required: false

auth:
  jwt_secret: "replace-with-at-least-32-random-characters"
  token_expire_hours: 24
```

---

### 实战：创建用户服务

**步骤 1：定义数据模型**

```go
package model

import "time"

type User struct {
    ID        uint      `gorm:"primaryKey"`
    Username  string    `gorm:"uniqueIndex;size:50"`
    Email     string    `gorm:"size:100"`
    Password  string    `gorm:"size:255"`
    Status    int       `gorm:"default:1"`
    CreatedAt time.Time
    UpdatedAt time.Time
}
```

**步骤 2：定义 Repository**

```go
package repository

import (
    "my-app/internal/model"
    "github.com/duiniwukenaihe/gin-bear/pkg/bear"
)

type UserRepository struct {
    bear.Repository[model.User]
}

func (r *UserRepository) Name() string { return "UserRepository" }
```

**步骤 3：定义 Service**

```go
package service

import (
    "context"
    "my-app/internal/model"
    "my-app/internal/repository"
)

type UserService struct {
    Repo *repository.UserRepository `inject:"-"`
}

func (s *UserService) Name() string { return "UserService" }

func (s *UserService) CreateUser(ctx context.Context, user *model.User) error {
    return s.Repo.Create(ctx, user)
}
```

**步骤 4：定义 Controller**

```go
package controller

import (
    "my-app/internal/model"
    "my-app/internal/service"
    "github.com/duiniwukenaihe/gin-bear/pkg/bear"
)

type UserController struct {
    Svc *service.UserService `inject:"-"`
}

func (c *UserController) Name() string { return "UserController" }

func (c *UserController) Build(b *bear.Bear) {
    b.Handle("GET", "/users", c.List)
    b.Handle("GET", "/users/:id", c.GetByID)
    b.Handle("POST", "/users", c.Create)
}

type CreateUserReq struct {
    Username string `json:"username" binding:"required"`
    Email    string `json:"email" binding:"required,email"`
    Password string `json:"password" binding:"required,min=6"`
}

func (c *UserController) Create(req *CreateUserReq) (*model.User, error) {
    user := &model.User{
        Username: req.Username,
        Email:    req.Email,
        Password: req.Password,
    }
    return user, c.Svc.CreateUser(nil, user)
}
```

**步骤 5：组装应用**

```go
package main

import (
    "context"
    "github.com/duiniwukenaihe/gin-bear/pkg/bear"
)

func main() {
    app := bear.Ignite()
    app.Beans(&UserRepository{}, &UserService{})
    app.Mount("/api/v1", &UserController{})
    app.EnableHealth()

    ctx := context.Background()
    app.ApplyAll(ctx)
    app.Launch(ctx)
}
```

---

### 核心功能

**依赖注入**

```go
type MyService struct {
    Repo *UserRepository `inject:"-"`
}

func (s *MyService) Name() string { return "MyService" }

// 获取 Bean
svc := bear.GetByType[*UserService]()
```

**Repository 泛型**

```go
type UserRepository struct {
    bear.Repository[User]
}

// 内置方法
repo.Create(ctx, user)
repo.FindByID(ctx, id)
repo.FindOne(ctx, cond)
repo.FindList(ctx, cond)
repo.Update(ctx, user)
repo.Delete(ctx, user)
repo.Count(ctx)
```

**Fairing 拦截器**

```go
type LoggingFairing struct {
    bear.BaseFairing
}

func (f *LoggingFairing) OnRequest(ctx *gin.Context) error {
    log.Info("请求开始", "path", ctx.Request.URL.Path)
    return nil
}

app.Attach(&LoggingFairing{})
```

**JWT 认证**

```go
jwtUtil := bear.NewJWTUtil(secret, expireHours)
token, err := jwtUtil.GenerateToken(userID, username)
app.Attach(bear.NewAuthFairing())
```

**接口限流**

```go
limiter := bear.NewMemoryRateLimiter(100, time.Second)
app.Use(bear.RateLimitMiddleware(limiter))
```

---

### 控制器签名

| 签名 | 说明 |
|------|------|
| `func(*gin.Context)` | 标准 Gin |
| `func(*gin.Context) (interface{}, error)` | 带错误处理 |
| `func(*Req) (*Res, error)` | 参数自动绑定 |
| `func() string` | 直接返回字符串 |
| `func() interface{}` | 直接返回 JSON |

---

### 模块系统

```go
type UserModule struct{}

func (m *UserModule) Name() string { return "UserModule" }

func (m *UserModule) Beans() []bear.Bean {
    return []bear.Bean{&UserRepository{}, &UserService{}}
}

func (m *UserModule) Build(b *bear.Bear) {
    b.Mount("/api/v1/users", &UserController{})
}

app.AddModule(&UserModule{})
```

---

### 常见问题

**Q: 控制器方法不执行？**

检查是否实现了 `Name() string` 方法：

```go
func (c *UserController) Name() string { return "UserController" }
```

**Q: 依赖注入失败？**

1. 检查是否调用了 `app.ApplyAll(ctx)`
2. 检查字段是否标记 `` `inject:"-"` ``
3. 检查 Bean 是否注册

**Q: 数据库连接失败？**

确认数据库已创建：

```bash
mysql> CREATE DATABASE myapp;
psql> CREATE DATABASE myapp;
```

---

## License

Apache License 2.0

# Gin-Bear

[English](README.md) | 简体中文

Gin-Bear 是基于 Gin 的应用脚手架与框架，源于
[goft-gin](https://github.com/shenyisyn/goft-gin) 的 Controller、IoC、Fairing
和响应处理模型，增加了面向生产的生命周期、配置、认证、健康检查、指标、
链路追踪与 OpenAPI 支持。

## 引入框架

在 Go 应用中安装当前稳定版本：

```bash
go get github.com/duiniwukenaihe/gin-bear@v0.9.5
```

运行时导入路径为 `github.com/duiniwukenaihe/gin-bear/pkg/bear`。
当前版本为 **v0.9.5**。升级已有应用前请阅读 [变更记录](CHANGELOG.md)、
[中英文升级说明](docs/upgrade-v0.9.4-to-v0.9.5.md) 和
[发布流程](docs/release-process.md)。

## 生成项目（可选）

`cmd/bear` 是项目生成器。创建新服务时可安装使用：

```bash
go install github.com/duiniwukenaihe/gin-bear/cmd/bear@v0.9.5
bear new my-service
```

已发布生成器携带同版本模板，将新项目的框架依赖固定为 `v0.9.5`，并拒绝
指定其他框架版本。已有应用无需安装生成器；不要通过重新生成覆盖业务代码。

在本地源码中验证尚未发布的模板时，显式使用当前源码：

```bash
go run ./cmd/bear new my-service \
  --framework-version dev \
  --framework-replace "$(pwd)"
```

该开发方式会写入本地 `replace`，不能作为已发布版本的兼容性证据。
未带版本的 HEAD 生成器会在创建项目之前拒绝 `--framework-version v0.9.5`。

## 推荐启动方式

新应用使用 `IgniteE`、返回错误的注册方法和 `Serve`，由进程负责信号处理，
统一记录启动或服务错误：

```go
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

func run(ctx context.Context) (resultErr error) {
	config := bear.NewSysConfig()
	config.SetFrameworkStrict(true)
	application, err := bear.IgniteE(config)
	if err != nil {
		return fmt.Errorf("initialize application: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, application.Shutdown(context.Background())) }()
	if err := application.EnableDatabaseE(ctx); err != nil {
		return fmt.Errorf("initialize database: %w", err)
	}
	if err := application.EnableRedisE(ctx); err != nil {
		return fmt.Errorf("initialize Redis: %w", err)
	}
	if err := application.HandleE("GET", "/hello", func() string { return "hello" }); err != nil {
		return fmt.Errorf("register hello route: %w", err)
	}
	if err := application.EnableHealthE(); err != nil {
		return fmt.Errorf("initialize health: %w", err)
	}
	if err := application.Serve(ctx); err != nil {
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
```

`Ignite`、链式注册和 `Launch` 继续保留以兼容 v0 源码。
禁用数据库时 `EnableDatabaseE` 不启动数据库；只有显式启用 Redis 或配置
`auth.storage_type: redis` 时，`EnableRedisE` 才启动 Redis。

## 可运行示例

这些示例作为源码由 `go test ./...` 编译，保持与框架 API 一致：

- [基本生命周期与可取消服务](examples/basic/main.go)：使用 `IgniteE`、
  `MountE`、`EnableHealthE` 和 `Serve`。
- [令牌撤销错误处理](examples/auth/main.go)：使用 `errors.Is` 判断
  `bear.ErrTokenRevocationUnavailable`。
- [生产配置加载](examples/migration/main.go)：启动前处理 `bear.LoadConfig` 错误。

运行 `go run ./examples/basic`，访问 `http://localhost:8080/api/hello`。

## 可选 gRPC 支持

通过 `GRPCServiceRegistrar` 注入服务，使用 `AddGRPCServiceE`、
`AddGRPCUnaryInterceptorE`、`AddGRPCStreamInterceptorE` 注册并处理错误。
启用前至少注册一个业务服务；gRPC 的认证放在一元和流拦截器中，HTTP Fairing
不处理 gRPC 请求。

gRPC 默认禁用。启用后标准健康服务默认开启，反射默认关闭。生产部署必须明确
选择进程 TLS、mTLS，或绑定回环地址、由同机 Nginx/Envoy 终止 TLS 的明文连接。
配置与限制见 [生产指南](docs/production.md#grpc)。

## v0.9.5 生产修复

本版修复令牌撤销绕过、缺失完整主键的乐观锁误更新、重叠就绪探测误报、
gRPC 健康订阅阻塞关闭、资源清理遗漏和迁移打包/恢复问题。行为变化见
[升级说明](docs/upgrade-v0.9.4-to-v0.9.5.md#简体中文)。

核心脚手架面向生产服务；上线前仍须使用实际部署的数据库和配置完成应用验收。
Agent 与 MCP 模块维持实验状态，独立管理版本。

## 生产与升级资料

- [生产指南](docs/production.md)
- [v0.9.1 到 v0.9.2 迁移指南](docs/migration-v0.9.1-to-v0.9.2.md)
- [兼容性约定](docs/compatibility.md)
- [运维手册](docs/runbook.md)
- [安全策略](SECURITY.md)
- [变更记录](CHANGELOG.md)

发布前使用项目指定的 Go 版本运行完整验证；联网执行时按生产指南显式启用联网：

```bash
GOSUMDB=sum.golang.org GOTOOLCHAIN=go1.26.6 make verify
```

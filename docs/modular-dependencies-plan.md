# 按需依赖与可组合运行时方案

目标是让业务服务只编译、下载它实际使用的功能，同时保留现有
`github.com/duiniwukenaihe/gin-bear/pkg/bear` 用法的兼容期。简洁程度以生成项目的
`go.mod`、`go list -deps` 和最终服务二进制为准，不以配置中关闭某功能为准。

## 已确认的现状

- 核心 `pkg/bear/db.go` 默认导入 PostgreSQL、MySQL、SQLite 驱动；运行时把
  `database.type` 设为 PostgreSQL，并不会让链接器移除另两个驱动。
- 核心 Casbin 实现使用 `casbin/gorm-adapter`。该适配器自身同时导入 PostgreSQL、
  MySQL、SQL Server、SQLite；因此仅把核心 SQLite 导入改为可选仍不能从二进制
  移除 SQLite。
- 2026-09-23 在 macOS arm64、Go 1.26.6 的本仓库 `./cmd` 样本中，默认二进制
  为 76,194,418 字节；同时排除 Casbin 和 SQLite 后为 69,439,554 字节。
  使用 `-ldflags='-s -w'` 时分别为 51,428,018 和 46,801,058 字节。数字只
  说明这个样本，不代表每个业务项目的体积。
- `devops1/devops-backend` 的生产 Go 文件没有直接导入 Casbin 或 SQLite，测试
  文件则直接使用 SQLite。因此生产服务可排除它们；若继续保留这些测试，模块
  清单仍会包含 SQLite 测试依赖。
- Agent 和 MCP 已经是独立 Go module，可继续维持按需引入。

## 第一阶段：兼容的二进制瘦身（当前候选）

保留默认构建行为。未使用 Casbin、SQLite 的服务可以使用
`-tags bear_no_casbin,bear_no_sqlite`。前者在编译时排除旧 Casbin API，后者
让选择 SQLite 的启动路径明确报错。只设置 `bear_no_sqlite` 仍会被 Casbin 的
通用 GORM 适配器带入 SQLite，所以需要两个标记同时使用。用
`go list -deps -tags ...` 验证依赖不在编译图中，用普通构建和精简构建各跑一次
启动验收。此阶段不声称清理 `go.mod`：默认构建和测试仍需要这些模块。

## 第二阶段：Casbin 与数据库适配器组合

先保持 `AuthorizationRequest` / `Authorizer` 接口稳定，再提供新的 Casbin
构造入口，让应用显式传入策略持久化适配器。Casbin 规则计算不负责选择业务
数据库驱动。PostgreSQL 适配器复用应用已经打开的连接池，并使用独立、受迁移
管理的 `casbin_rule` 表；MySQL 和 SQLite 适配器按需实现。不能以引入另一套
数据库连接池、自动建库或在启动时隐式迁移来换取表面上的依赖减少。现有
`NewCasbinEnforcer(*GormAdapter, ...)` 和 `NewCasbinAuthorizer(*GormAdapter, ...)`
在兼容期保留，由旧包实现。

这一阶段的验收条件是：Casbin + PostgreSQL 的业务二进制可以通过
`go list -deps` 证明没有 SQLite/SQL Server 驱动；现有 `casbin_rule` 数据无需
重建；跨实例撤权、策略写入失败关闭、并发、迁移和回滚均在真实 PostgreSQL
上通过。适配器选择以正确性和维护成本为先，不为减几 MB 复制一份未经充分
测试的 Casbin 持久化实现。

## 第三阶段：新项目默认按需生成

把无数据库驱动的 HTTP/配置/生命周期运行时形成独立 Go module。生成器明确
选择 `postgres`、`mysql` 或 `sqlite` 数据库模块及可选 Casbin 模块；不选择时
生成项目不导入相应模块，也不在 `go.mod` 中声明它。默认模板保持无数据库
可启动；生产 profile 仅给出可选配置示例。继续维护旧 `pkg/bear` 兼容入口，
通过新构造器或模块注册逐步迁移，避免一次性破坏 v0.9.x 消费者。

生成器需把选择写入 scaffold manifest；预览与实际生成使用同一份渲染结果。
重新生成时只显示差异，不自动删除业务代码、依赖或迁移。每个组合至少检查
`go mod tidy -diff`、编译、启动、数据库迁移、鉴权及关闭；特别验证
“PostgreSQL + Casbin、无 SQLite”和“仅 HTTP、无数据库/Casbin”。最终门禁同时
检查二进制依赖图与生成项目的 `go.mod`，防止只是配置关闭了功能却仍下载或
链接不需要的包。

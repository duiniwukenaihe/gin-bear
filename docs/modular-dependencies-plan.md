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

## 依赖盘点与拆分边界

| 当前入口 | 主要依赖 | 拆分方向 |
| --- | --- | --- |
| HTTP、配置、生命周期 | Gin、validator、YAML | 留在精简核心；不要让核心引用可选功能的具体类型 |
| 数据库与迁移 | GORM、PostgreSQL、MySQL、SQLite 驱动 | 每种驱动显式导入；迁移只依赖已选数据库 |
| 鉴权 | JWT、Casbin、Casbin GORM adapter | JWT 与 Casbin 独立；Casbin 持久化按数据库选适配器 |
| 缓存与作业 | go-redis、Redis OTel、cron | Redis、追踪集成和 cron 分别可选 |
| 服务接口 | gRPC、WebSocket、OpenAPI | 从核心的启动和路由实现中移出具体依赖 |
| 观测 | Prometheus、OpenTelemetry SDK/导出器 | 指标、追踪和导出器按需组合 |
| 开发工具 | Cobra、fsnotify、x/mod | 只由 CLI/生成器使用，不进入业务服务编译图 |
| AI | Eino Agent、MCP SDK | 保持独立模块，只有显式引入才进入业务模块图 |

目前 `pkg/bear` 这个单一包同时引用上述多种运行时依赖；Go 按**包**编译，
所以只在配置中关闭功能并不能移走其依赖。生成应用的 `app.Run` 还无条件调用
数据库、Redis、追踪和指标启用方法。精简方案必须改变源码导入和生成结果，
而不是增加一组 `enabled: false`。`go.mod` 中的 sqlmock、miniredis 属于测试依赖，
Cobra、fsnotify 属于开发工具；它们与业务二进制的编译依赖应分别统计。

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

把无数据库驱动的 HTTP/配置/生命周期运行时形成独立 Go module。先按依赖族
拆包，不为每个小工具创建一个 module：数据库驱动、Casbin、Redis、gRPC、
观测和 Agent 这些重依赖需要可独立选择。功能通过显式构造/注册接入，不能
依赖空白导入或全局副作用注册。生成器明确选择 `postgres`、`mysql` 或
`sqlite` 数据库模块及可选功能；不选择时生成项目不导入相应模块，也不在
`go.mod` 中声明它。模板只调用选中功能的启动方法。兼容期保留现有 `bear new`
行为，新增显式精简 profile；例如选择 PostgreSQL 与 Redis 时只生成这两项
接线。精简 profile 在不选数据库时也能启动；生产 profile 只给出已选功能的
配置示例。继续维护旧 `pkg/bear` 兼容入口，
通过新构造器或模块注册逐步迁移，避免一次性破坏 v0.9.x 消费者。

生成器需把选择写入 scaffold manifest；预览与实际生成使用同一份渲染结果。
重新生成时只显示差异，不自动删除业务代码、依赖或迁移。每个组合至少检查
`go mod tidy -diff`、编译、启动、数据库迁移、鉴权及关闭；特别验证
“PostgreSQL + Casbin、无 SQLite”和“仅 HTTP、无数据库/Casbin”。最终门禁同时
检查二进制依赖图与生成项目的 `go.mod`，防止只是配置关闭了功能却仍下载或
链接不需要的包。

## 发布与验收边界

`extensions/agent/go.mod` 当前依赖主模块 `v0.0.0`，并用 `replace => ../..`
在仓库内构建。依赖方不会继承这个本地替换；发布前必须让 Agent 要求一个已发布
且兼容的主模块版本，随后从仓库外创建干净消费项目，**不使用 replace** 验证
`go mod tidy`、构建和启动。MCP 独立模块也做同样的外部消费检查。开发时可以
用 `go.work` 指向本地源码，但不能把本地路径当成发布依赖。

每阶段记录三种不同结果：`go.mod` 模块图、`go list -deps` 编译图、服务二进制
大小。验收至少覆盖 HTTP-only、PostgreSQL-only、MySQL-only、SQLite-only、
PostgreSQL+Casbin，以及加入 Redis/追踪/Agent 的组合；无关驱动和功能必须从
对应编译图中消失。不要追求所有组合的全排列，优先覆盖共享接口和高风险交叉点。

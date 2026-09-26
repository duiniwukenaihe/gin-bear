# Upgrade from v0.9.4 to v0.9.5 / 升级到 v0.9.5

English | [简体中文](#简体中文)

`v0.9.5` is a compatible maintenance release for the root framework and
generator. Go 1.26.6 remains required. No framework database schema migration
or configuration rename is introduced. Application acceptance is still required.

## Update and validate

```sh
go get github.com/duiniwukenaihe/gin-bear@v0.9.5
go mod tidy
go test ./...
go install github.com/duiniwukenaihe/gin-bear/cmd/bear@v0.9.5
```

Install the generator only if needed. New projects pin the matching framework
version. For an existing app, review generated changes in a scratch copy;
do not regenerate over application code. Earlier upgrades must also follow
the [v0.9.4 guide](upgrade-v0.9.3-to-v0.9.4.md).

## Behavior changes to check

- JWT signatures must use canonical Base64URL without padding or CR/LF.
  Normally issued tokens and existing revocation keys remain compatible.
  Redis-backed authentication returns HTTP 503 if its required manager/store
  is missing. Verify login, logout, revoked tokens, and Redis failure handling.
- Optimistic repository updates require every primary-key field, including
  composite keys. Missing keys return `gorm.ErrPrimaryKeyRequired` before
  changing the entity version or writing SQL. Populate the complete identity.
- Overlapping readiness probes share the current dependency check and retain
  independent cancellation/deadlines. Hung dependencies still time out.
- Persistent gRPC health watches receive `NOT_SERVING` and finish on shutdown.
  Confirm client reconnect behavior and the application's business RPC drain.
- Strict lifecycle shutdown/rollback closes prestarted resources whose bean
  registration was removed or replaced. Do not reuse such resources after stop.

## Generated deployment files

Existing projects need to review and copy the updated production Dockerfile:
the image now includes `migrations/` from the same revision as its binaries.
Build and deploy the server and migration tool together; supply configuration
and secrets at deployment. UID 65532 must be able to read mounted configuration.
The base configuration disables the database. Run migrations as a separate job.

The generated migration command now supports `-force-version` and
`-force-applied=true|false`. These flags change migration history only.
Inspect the real schema first: choose `true` only for a fully applied version,
or `false` only if absent/manually rolled back. Never use force to hide a failed
migration. Review the template change before updating an existing command.

## Acceptance and rollback

On a disposable database using the production engine/transport, verify CRUD,
optimistic conflicts, migrations and recovery, auth/revocation, readiness,
and clean shutdown. Rebuild images when using the production Dockerfile.
Keep backups and the previous deployment available. A binary rollback does
not reverse SQL and may reintroduce fixed security defects; prefer a forward
fix and review any rollback separately. Published tags are immutable.

Agent and MCP modules remain experimental, with independent module versions.
This root release does not publish their directory-prefixed tags.

## 简体中文

`v0.9.5` 是框架和生成器的兼容性维护版本，继续要求 Go 1.26.6。
本次不引入框架数据库结构迁移，也不重命名配置项。升级命令见上文；
已有应用仍须完成业务验收。生成器按需安装，已有项目应在临时副本中比较
模板差异，避免覆盖业务代码。从更早版本升级时也要阅读上一版升级说明。

### 需要检查的行为变化

- JWT 签名只接受规范的 Base64URL，拒绝补位和回车换行。正常签发的令牌、
  已有撤销记录继续兼容。使用 Redis 撤销存储时，缺少管理器或存储会返回
  HTTP 503。验证登录、退出、令牌撤销和 Redis 故障场景。
- 乐观锁更新要求完整主键，包括复合主键。缺失时返回
  `gorm.ErrPrimaryKeyRequired`，不会改版本号或执行写入。
- 重叠的就绪探测共享正在执行的依赖检查，各自保留取消和超时控制。
  卡住的依赖仍会触发超时。
- gRPC 健康订阅在关闭时收到 `NOT_SERVING` 并结束；验证客户端重连和
  业务 RPC 的排空过程。
- 严格生命周期会关闭已预启动但随后移除或替换注册的资源；关闭后不要复用。

### 部署模板与迁移恢复

使用生产镜像的已有项目应审阅新版 Dockerfile，将同一源码版本的
`migrations/` 连同服务端和迁移程序打包。配置和密钥由部署环境注入；
挂载配置应允许 UID 65532 读取。基础配置默认禁用数据库，迁移仍需单独执行。

生成的迁移程序新增 `-force-version` 和 `-force-applied=true|false`，
只修改迁移历史。先检查实际数据库结构：完整执行过才能选 `true`，
尚未执行或已经人工回滚才能选 `false`。不能用强制标记掩盖迁移失败。
已有项目需审阅模板差异后再更新迁移程序。

使用与生产一致的数据库引擎和连接方式，在临时数据库上验收 CRUD、乐观锁
冲突、迁移与恢复、认证与撤销、就绪检查和正常退出。使用镜像时重新构建并
验收镜像。保留备份与上一部署；回滚程序不会回滚 SQL，并可能重新引入已修复
的安全问题，应优先向前修复，单独评审回滚。已发布标签不能移动。

Agent 与 MCP 仍是独立版本的实验模块；本次根模块发布不替它们发布标签。

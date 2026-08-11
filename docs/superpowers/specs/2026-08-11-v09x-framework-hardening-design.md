# gin-bear v0.9.x 生产框架补强设计

## 1. 目标与基线

本轮目标是把 gin-bear 作为 Go + Gin 生产脚手架继续补强，同时保持 `v0.9.x` 兼容性，
不提前进入 `v0.10.0`。这是一条连续完成的加固开发线，不把所有行为变更伪装成单个补丁：

- `v0.9.2` 候选：请求终止、IoC/Lifecycle、服务状态机及运行时安全；
- `v0.9.3` 候选：资源权限契约、脚手架生成闭环及版本治理。

两段候选在同一隔离分支上串行实现和验证，但分别保留可审查的规格、提交边界和兼容门禁。
在两段都完成前不做“全部完成”的结论；本轮也不创建版本 tag。

唯一开发基线是：

- 分支起点：`4dbc3d12ece9858976c6c28e0cea54235a81b8bd`
- 基线分支：`codex/production-framework-v010`
- 本轮分支：`codex/v09x-framework-hardening`
- 版本目标：未发布的 `v0.9.2` 和 `v0.9.3` 候选
- Go 工具链：继续使用 `1.25.12`

本轮只在最新基线上增加差量，不回到 `main` 或远端 `v0.9.1` 重做已经完成的工作。
以下能力视为基线资产并保持不动：普通 HTTP Fairing 的 Abort 终止、类型化 HTTP
错误、应用级 Runtime/Container/Lifecycle、确定性初始化与逆序关闭、HTTP 超时和请求
大小限制、CORS allowlist、Controller 拦截器隔离、插件启动后拒绝热重载、迁移和
OpenAPI 加固、数据和认证安全、日志脱敏、CLI 原子目录发布、发布验证脚本。

## 2. 成功标准

完成后必须同时满足：

1. 已写出 401/403/其他响应或调用 `ctx.Abort()` 后，HTTP、Controller、WebSocket
   三条链路都不会继续执行后续 Fairing 或业务处理器，也不会追加第二段 JSON。
2. URI 参数是资源标识的最终权威值，JSON、表单或查询参数不能覆盖它。
3. 严格模式下，缺失依赖、重复具体 Bean、接口多实现歧义均在启动期返回可诊断错误。
4. Module/Controller 构建期间发现的 Bean 会在路由可服务前完成注入、初始化并纳入关闭。
5. 初始化失败会清理失败组件和已启动组件；关闭超时后再次调用 Stop 能继续未完成工作。
6. 同一 Bear 只允许一个服务循环；并发 Serve/Launch 不会让失败调用关闭正在运行的实例。
7. 生产配置拒绝 WebSocket 通配 Origin 和全网可信代理，并限制 JWT 和 WebSocket 资源。
8. Casbin 和令牌撤销不再依赖跨应用全局容器或 `context.Background()` 请求替代品。
9. 新脚手架默认启用严格契约和统一响应，`bear gen api` 后无需手工注册模块即可编译测试。
10. 旧项目默认保持兼容模式，现有公开 API 继续编译；安全终止修复不受兼容模式影响。
11. 不引入 Docker、Compose、Kubernetes 或其他部署编排资产。

## 3. 兼容策略

### 3.1 配置开关

为保证 `v0.9.x` 的源码兼容，本轮不向 `SysConfig`、`AuthConfig`、`JWTConfig` 或
`WebSocketConfig` 增加字段。公开结构体新增字段会破坏外部包的无键复合字面量，不属于严格
兼容新增。新策略继续使用现有 `SysConfig.Config` 的字符串扩展键，并由新增 accessor 统一
解析；业务代码不直接断言 map 值。

`framework.response_mode` 仅接受 `raw` 和 `envelope`。旧配置缺少这些键时等价于：

```yaml
config:
  framework.strict: false
  framework.response_mode: raw
```

新脚手架明确生成：

```yaml
config:
  framework.strict: true
  framework.response_mode: envelope
```

`config["framework.strict"]` 是运行时契约开关，与 `config["strict"]` 不同：后者只控制
配置文件是否拒绝未知字段，继续保持当前语义和生产环境强制规则。两者不互相覆盖，也不复用
同一个默认值。新增 `FrameworkStrict()`、`ResponseMode()` 和对应 Set 方法作为公开访问入口。

严格模式控制依赖完整性、Fairing 完整嵌套、错误返回式构建和启动状态检查。以下安全修复
始终生效，不允许退回兼容行为：Abort/已写响应终止、URI 最终覆盖、禁止重复响应、生产
WebSocket Origin 校验、生产可信代理校验、JWT 长度限制和请求 Context 传播。

行为边界固定如下：

| 能力 | 兼容模式 | 严格模式 |
| --- | --- | --- |
| Abort、已写响应、URI 权威、安全配置、Casbin 应用隔离 | 修复后行为 | 修复后行为 |
| Fairing 顺序 | 保持历史顺序 | 完整正向进入、反向退出 |
| 缺失/重复/歧义依赖 | 保持旧 API 行为并记录诊断 | ApplyAll 启动失败 |
| Module/Controller 与 Init 顺序 | 保持历史顺序 | 先完整构建依赖图和路由，再 Init |
| Init 失败后的再次 ApplyAll | 返回缓存错误 | 清理成功后允许重试 Init |
| 失败 Initializer 自身清理、可续 Stop | 保持历史语义 | 启用新生命周期语义 |
| 自动成功响应 | 由 response_mode 决定 | 由 response_mode 决定 |

### 3.2 公开 API 兼容

- 保留 `Ignite`、`Apply`、`Resolve`、`Beans`、`AddModule`、`Mount`、`Launch` 和
  `FairingHandler.OnResponse`。
- 新增错误返回式 API；旧 API 作为兼容包装，不删除、不改签名。
- 旧 API 的弃用说明必须指出替代 API 和失败语义，但本轮不移除它们。
- `v0.9.2` API 差异门禁以 `v0.9.1` 为基线，`v0.9.3` 以通过门禁的 `v0.9.2` 本地快照为
  基线；只允许新增标识符和方法，不新增公开结构体字段。
- 旧项目的裸对象、裸数组和自写 `gin.Context` 响应继续可用；统一包络只在
  `response_mode: envelope` 下自动应用。

## 4. 请求管线与响应契约

### 4.1 统一终止判断

内部只保留一个请求终止谓词：Context 为 nil、`ctx.IsAborted()` 或
`ctx.Writer.Written()` 任一成立即为终止。每个 Fairing 调用前后都检查该谓词。

该规则覆盖：

- 全局 Fairing；
- Controller `IInterceptors`；
- 路由级 Fairing；
- 普通反射 Handler；
- 直接写响应的 `gin.HandlerFunc`；
- WebSocket Upgrade 前的 Fairing。

Fairing 返回 error 时调用统一 `WriteError`。若响应尚未提交，按类型化错误写一次；若响应
已经提交，只记录结构化错误并 Abort，不能改状态码或追加正文。Recovery 遇到已提交响应
时采用同样规则，只记录 panic、request id 和路由，不再写第二段 JSON。

### 4.2 严格 Fairing 嵌套

兼容模式保持现有可观察顺序，只补终止语义。严格模式使用真正的嵌套顺序：

```text
请求：global -> controller -> route -> handler
响应：handler -> route -> controller -> global
```

只有成功进入过请求阶段的 Fairing 才参与对应响应阶段。某层请求阶段终止后，不执行更内层
逻辑；已经进入的外层是否执行响应阶段，以是否存在可转换的业务结果为准。直接写字节的
Gin Handler 没有可转换结果，因此不调用 `OnResponse`，但仍执行终止检查和错误日志。

新增：

```go
func (f *FairingHandler) OnResponseE(result any) (any, error)
```

框架内部只调用 `OnResponseE`。旧 `OnResponse` 保留并标记 Deprecated，继续兼容“忽略转换
错误并返回最后成功结果”的历史行为。

### 4.3 绑定优先级

结构体请求按以下顺序绑定并只在全部绑定后验证：

```text
query -> form/body -> URI -> validator
```

因此带 `uri` 标签的字段最终总是路由值。若 URI 缺失或类型转换失败，返回稳定的 400
客户端错误；日志保留底层原因，响应不泄露解析细节。独立的标量路径参数继续按当前位置规则
绑定。

### 4.4 JSON 提交与 HTTP 状态

成功响应在写入状态码前完成 JSON 编码。编码失败时返回统一 500；不得出现“HTTP 200 +
半段 JSON”。实现使用内存缓冲完成编码，成功后一次性提交 Content-Type、状态码和正文。

新增显式状态包装：

```go
type StatusResponse struct {
	Status int
	Value  any
}

func WithStatus(status int, value any) StatusResponse
```

`Status` 必须是 200 到 599，否则在写响应前转为内部错误。`raw` 模式直接编码 `Value`；
`envelope` 模式下，普通值包装为 `Response{Code: status, Message: http.StatusText(status),
Data: value}`，已有 `Response` 保持其 code/message/data。普通成功值默认使用 HTTP 200 和
`Response{Code: 200, Message: "success", Data: value}`。类型化错误继续决定 HTTP status
和业务 code。204、304 和 HEAD 响应不编码正文，不受 envelope 模式影响。

## 5. IoC、构建与生命周期

### 5.1 严格依赖 API

新增：

```go
func ResolveE[T any](factory *BeanFactory) (T, error)
func (f *BeanFactory) ApplyE(obj any) error
func (b *Bear) BeansE(beans ...Bean) error
func (b *Bear) AddModuleE(modules ...Module) error
func (b *Bear) MountE(group string, classes ...IClass) error
```

`ApplyE` 必须报告对象不是可注入结构、未导出 inject 字段、缺失依赖、接口多实现歧义和
配置值注入错误。`ResolveE` 的错误包含请求类型及 missing/ambiguous 分类，不包含 Bean
内部敏感值。

Bean 规则固定为：

- 相同注册类型和相同实例重复注册是幂等操作；
- 相同具体类型注册不同实例在严格模式下是错误；
- `TrySetWithInterface` 的显式接口绑定优先于隐式实现扫描；
- 没有显式绑定且只有一个实现时允许解析；
- 没有显式绑定且有多个实现时返回歧义错误，不按 map 或注册顺序猜测；
- 兼容模式继续允许旧 Set/Beans 覆盖，但容器记录冲突，切换到严格 ApplyAll 时必须失败。

静态注入器的新键使用完整包路径加类型名，避免不同包同名结构体碰撞。现有仅类型名注册
保留为全局兼容回退；应用级 Runtime 和框架内置组件不得使用该回退。新增返回 error 的
`RuntimeStaticInjectorE` 与注册函数。严格 ApplyE 优先使用 E 注入器；只有旧注入器时回退到
当前容器的反射注入，以便执行完整缺失/歧义校验，不能调用进程级静态注入器。

### 5.2 错误返回式构建

保留 `Module.Build` 和 `IClass.Build`，并增加可选接口：

```go
type ModuleBuilderE interface {
	BuildE(*Bear) error
}

type ClassBuilderE interface {
	BuildE(*Bear) error
}
```

`ApplyAll` 检测到 E 接口时始终优先调用 `BuildE`，与模块通过 AddModule 还是 AddModuleE
注册无关；只有未实现 E 接口时才调用旧 Build。`AddModuleE`/`MountE` 返回注册错误，后续
BuildE 错误由 ApplyAll/Serve 返回。新生成的 Module/Controller 实现 E 接口；为满足旧 Module
和 IClass 接口也保留 Build 方法，但框架正常启动路径不会调用该包装。

### 5.3 启动阶段

严格启动按固定阶段执行：

1. 关闭插件注册入口并等待在途注册结束。
2. 注册 Module 自身及其 `Beans()`，记录稳定注册序列。
3. 注入 Module，执行 Module BuildE/Build，收集 Mount 和新 Bean。
4. 对稳定序列执行严格依赖解析和字段注入。
5. 注入 Controller 及其 Fairing，执行 Controller BuildE/Build，完成路由注册。
6. 再次检查构建期间新增 Bean，重复注入直到没有新增项；最多允许 32 轮，超限返回
   `ErrBuildRegistrationLoop`。
7. 校验容器冲突和全部 inject 字段，封闭 Bean 与生命周期注册。
8. 按注册顺序执行 Init；全部成功后才允许 HTTP/gRPC Serve。

上述顺序由严格模式启用。兼容模式保持当前“注入、Init、Module Build、Controller Build”
顺序，避免 `v0.9.x` 内改变依赖初始化时机；启动时记录迁移警告。两种模式都保证路由构建
只发生一次。严格模式重试初始化时复用已经构建的依赖图和路由，不重复注册路由。

### 5.4 失败、重试与关闭

严格 Lifecycle 在调用 Init 前将当前条目标记为需要清理。Init 返回错误或 panic 时：

1. 先清理失败组件本身；
2. 再按逆注册顺序清理此前成功组件；
3. 聚合启动错误和清理错误；
4. 只有全部清理完成，初始化阶段才可由下一次 ApplyAll/Serve 重试。

Module/Controller 构建错误因 Gin 路由无法可靠撤销，属于终态错误；同一个 Bear 不允许重试
构建。严格模式的初始化错误属于可重试错误，但组件必须遵守 Init/Shutdown 可重复进入契约。
兼容模式继续缓存第一次 ApplyAll 错误，保持现有调用方可观察行为。

严格 Lifecycle 为每个组件记录 `pending`、`stopping`、`retry_pending`、`stopped` 和
`stopped_with_error`。Stop 的 Context 到期时：

- 未开始的组件保持 pending；
- 旧 `Shutdowner` 在独立 worker 中运行；调用方超时后条目保持 stopping，worker 完成时转为
  stopped 或 stopped_with_error，后续 Stop 只等待完成通道，不重复调用；
- `ContextShutdowner` 在 Context 取消后返回 context 错误时转为 retry_pending，后续 Stop 用新
  Context 再调用；返回 nil 时转为 stopped，返回非 context 错误时转为 stopped_with_error；
- 再次 Stop 先处理逆序位置最靠后的 retry_pending，再继续 pending；
- stopped_with_error 不自动重试，避免对未知非幂等关闭操作重复执行，但错误保留到最终结果；
- 所有组件离开 pending、stopping 和 retry_pending 后，Lifecycle 才进入 stopped；
- 每次返回聚合本次已知错误，并保留历史错误用于最终诊断。

Start 在每个组件前检查 Context；取消后不再启动新组件，并执行相同回滚。

### 5.5 服务状态机

新增不接管系统信号的入口：

```go
func (b *Bear) Serve(ctx context.Context) error
```

`Serve` 负责等待/执行 ApplyAll、绑定监听器、服务和优雅关闭。每个 Bear 同时只能有一个
Serve 所有者；第二个 Serve/Launch 返回 `ErrAlreadyServing`，不能触发 Lifecycle.Stop。
`Launch` 保留为兼容包装，内部创建 SIGINT/SIGTERM Context 后调用 Serve。新脚手架由
`cmd/server` 持有信号 Context，并调用 `Serve`，消除双重 signal.Notify。

新增 `IgniteE(args ...any) (*Bear, error)`，配置解析、生产安全校验、可信代理和 Gin 运行时
冲突都通过 error 返回；旧 `Ignite` 调用 `IgniteE` 并保持 panic 兼容。

Gin mode 和默认 writer 是进程级全局状态。严格模式在进程内登记首个 mode/writer 组合；
后续严格 Bear 使用相同组合时允许创建，使用冲突组合时由 `IgniteE` 返回
`ErrGinRuntimeConflict`，不能静默覆盖。兼容模式维持现有行为并记录警告。框架自身日志始终走
应用 Runtime logger，不依赖 Gin 全局 writer。

## 6. HTTP、认证与 WebSocket 安全

### 6.1 生产配置拒绝项

`Validate`/`IgniteE` 在 production/release 模式完成只依赖配置的校验并拒绝：

- `websocket.allowed_origins` 包含 `*`；
- `server.trusted_proxies` 包含覆盖全部 IPv4 或 IPv6 的 CIDR，包括等价规范化形式；
- JWT 最大长度、WebSocket 连接数或超时超出下文明确上限。

“严格模式注册了 WebSocket 路由但没有明确 Origin allowlist”依赖路由元数据，不能在
IgniteE 阶段判断。该检查在严格 ApplyAll 的路由构建完成后、Lifecycle.Init 之前执行；直接
HandleWS 和 Module/Controller Build 注册的路由都计入当前 Bear 的 WebSocket 路由计数。

开发模式可保持同源默认。生产模式必须是明确 Origin allowlist，不接受反射式或任意 Origin。

### 6.2 WebSocket 策略与连接上限

继续使用现有扩展键：`websocket.max_message_bytes`、`websocket.read_timeout`、
`websocket.write_timeout`、`websocket.ping_interval`，并新增
`websocket.max_connections`。固定规则为：

| 设置 | 默认值 | 允许范围 |
| --- | --- | --- |
| handshake_timeout_ms | 10000 ms | production 为 100 ms 到 30000 ms |
| max_message_bytes | 1 MiB | 1 字节到 16 MiB |
| read_timeout | 60s | 1s 到 5m |
| write_timeout | 10s | 100ms 到 1m |
| ping_interval | 30s | 1s 到 5m，且必须小于 read_timeout |
| max_connections | 严格/production 为 1024 | 1 到 100000 |

兼容开发模式未配置 `max_connections` 时保持不限；显式配置 0 或负数均为错误。连接计数归属
Bear Runtime，在 Upgrade 前原子占位，超过上限返回 503；Upgrade 失败或连接关闭必须释放
名额。

WebSocket 在 Fairing 终止、服务关闭或连接超限时都不得 Upgrade。现有 read limit、deadline、
ping/pong 和 hijacked connection 关闭能力继续保留。

### 6.3 JWT 与撤销 Context

`v0.9.x` 固定 JWT 最大长度为 16 KiB，不增加公开配置字段，也不为单一安全边界引入额外
配置。`JWTUtil.ParseToken` 在解析前检查字节长度，并使用一次 `ParseWithClaims` 完成算法、
签名、exp、issuer、audience 和 clock skew 校验，不再先执行 `ParseUnverified`。可配置上限
留到允许调整公开配置类型的未来主版本重新设计。

`AuthTokenManager` 新增：

```go
func (m *AuthTokenManager) ParseTokenContext(ctx context.Context, token string) (*CustomClaims, error)
```

`JWTFairing` 传入 `ctx.Request.Context()`。旧 `ParseToken` 保留并以 Background 调用新方法，
仅用于兼容非请求代码。Redis blacklist 的 GET/SET 全程使用调用方 Context。

### 6.4 Casbin 与资源权限

`CasbinFairing` 只使用注入到当前 Runtime 的 Enforcer，删除 `GetByType` 全局回退。这是防止
跨应用权限对象污染的强制安全修复，在兼容模式也生效。迁移方式是把 CasbinFairing 作为当前
Bear 的 Bean/Attach 项在 ApplyAll 前注册；缺失注入在严格模式启动失败，在兼容模式请求时
返回通用 500。Enforce 内部错误写入结构化日志，对客户端统一返回不含底层原因的 500；拒绝
仍返回 403。

新增与存储实现无关的权限契约：

```go
type AuthorizationRequest struct {
	Subject  string
	Resource string
	Action   string
	Scope    map[string]string
}

type Authorizer interface {
	Authorize(context.Context, AuthorizationRequest) (bool, error)
}

type ScopeResolver func(*gin.Context) (map[string]string, error)
type SubjectResolver func(*gin.Context) (string, error)
```

该新增契约属于 `v0.9.3` 候选；`v0.9.2` 只完成现有 CasbinFairing 的应用隔离和错误脱敏。

`NewPermissionFairing(resource, action string, scope ScopeResolver)` 创建 Fairing；
`SetSubjectResolver` 可替换默认 Subject 解析。Authorizer 通过当前 Bear Container 注入。
ScopeResolver 从 URI 和已认证 Context 生成 scope；Subject 默认来自 `current_user_id`。缺少
Subject 返回 401，resolver 错误或 Authorizer 错误记录后返回通用 500，拒绝返回 403。Scope
在调用前复制，避免后续中间件修改。现有 Casbin subject/path/method Fairing 保留，二者不
隐式混用。

## 7. 脚手架闭环

### 7.1 项目标记和模块注册

新项目增加 `.bear/scaffold.json`，固定 schema 1，记录：

- 项目 module path；
- framework 版本和 template 版本；
- 已生成 API 的原始名称、包名、相对路径和 Module 类型；
- 框架管理文件的 SHA-256。

新项目包含框架管理的 `internal/app/modules_gen.go`。它只由 manifest 生成，返回稳定排序的
`[]bear.Module`。`app.Run` 先通过 `AddModuleE` 注册这些模块，再执行用户拥有的
`configure(application)`。生成器改写前校验记录的文件 hash；用户改过管理文件时明确失败，
不覆盖其内容。

在脚手架项目中执行 `bear gen api` 时，生成器从当前目录向上寻找最近的 `go.mod` 和
`.bear/scaffold.json`，自动更新资源包、module registry、manifest、`go.mod` 和 `go.sum`。
在只有 `go.mod`、没有 manifest 的旧项目中，继续生成包但不猜测应用入口，并输出明确的
手工注册代码，保持兼容。

### 7.2 文件事务

一次 API 生成是持锁、可恢复的失败原子命令，不宣称文件系统提供跨多个文件的瞬时原子提交：

1. 以 `O_CREATE|O_EXCL` 获取 `.bear/generate.lock`；锁存在时拒绝并给出锁路径，不猜测陈旧锁。
2. 在项目根目录同文件系统建立 staging，渲染并 gofmt 资源包、module registry 和 manifest。
3. 保存 resource 目录、module registry、manifest、go.mod 和 go.sum 的原始存在状态、bytes、
   mode 与 SHA-256，并写 `.bear/generate-journal.json`。
4. 用 `modfile` 向 staging go.mod 添加生成代码已知的直接依赖：Gin `v1.12.0`、GORM
   `v1.26.0`，使用 decimal 字段时再加 decimal `v1.4.0`。这些常量由测试与仓库 go.mod
   对齐；生成代码不直接 import 数据库 driver，因此不额外提升 driver 为直接依赖。
5. 对 staging modfile 执行 `go mod download -modfile=<staging.mod> all`，得到 staging sum；
   不调用会扫描尚未发布源码的 `go mod tidy`。
6. 校验所有待发布文件，再次比较目标 hash；按“新资源目录、go.mod/go.sum、module registry、
   manifest”顺序发布，registry 发布前生成包不会进入应用构建图。
7. 任一步失败时，仅当当前目标 hash 仍等于本命令写入值才按 journal 回滚；发现外部并发修改
   时停止自动覆盖并保留 journal，返回逐文件恢复说明。
8. 全部成功后删除 journal 和 lock。进程崩溃遗留 journal/lock 时，下一次 gen 只执行校验式
   recovery，恢复完成后才允许新生成。

生成期间若 Context 取消，也执行同样回滚。命令只覆盖生成器声明的目标文件，不还原用户的
其他文件；不遵守 `.bear/generate.lock` 的外部读取者可能短暂看到发布中间态，因此文档只
承诺失败后可恢复和不覆盖并发编辑，不承诺多文件系统事务隔离。

### 7.3 CRUD 语义

- Update DTO 的指针字段保留字段类型校验但移除 `required`，因此显式 `false`、`0` 和空值
  按字段规则处理，不会被误认为未提供。
- 空更新返回稳定 400，不再静默成功。
- Update/Delete 检查 `RowsAffected`；目标不存在返回类型化 404。
- Get 对 GORM not found 统一映射 404，不泄露 SQL。
- 新 Controller 使用路径标量参数或带 `uri` 标签的请求，不再自行从 Context 重复解析 ID。
- 新 Controller 返回统一 envelope；Create 使用 `WithStatus(201, value)`，Delete 使用 204
  且无响应正文。

### 7.4 配置、迁移和端到端验收

新 `app.Run` 使用 `LoadConfig` + `IgniteE`，错误逐层返回，不使用配置 panic 路径。新模板由
`cmd/server` 唯一持有信号 Context，并调用 `Serve`。

`bear gen api` 增加可选 `--dialect mysql|postgres|sqlite`。只有明确给出 dialect 才生成 up/down
SQL；未指定时不猜数据库，也不生成与实际 dialect 不一致的 SQL。字段到 SQL 类型映射按 dialect 固定并
测试，不支持的组合在发布任何文件前失败。

真实脚手架验收流程为：

```text
bear new -> 配置本地未发布版本 replace -> bear gen api -> go test -mod=readonly ./...
```

测试中的 replace 仅用于本仓库尚未发布的 `v0.9.3`；流程不得手工修改 module registry、
不得手工 `AddModule`、不得手工 `go mod tidy`。另有 SQLite HTTP E2E 覆盖 create/list/get/
update/delete、参数校验、404 和统一响应包络。

## 8. 版本与文档治理

CHANGELOG 分别记录未发布的 `v0.9.2` 核心候选和 `v0.9.3` 脚手架候选。README、production
guide、runbook、migration guide、示例和 CLI development fallback 在第一段完成时指向
`v0.9.2`，第二段完成时统一前移到 `v0.9.3`；不能提前声称后一个候选已经完成。本轮不创建
tag、不 push、不合并 main。

`.bear/scaffold.json` 使后续工具可以判断模板来源，但 `v0.9.3` 只实现读取、校验和更新
manifest，不实现自动重写用户代码的 upgrade 命令。自动升级需要独立设计，避免把本轮变成
不可控的 Kitchen Sink 重构。

竞品取舍仅吸收与本轮直接相关的模式：[Uber Fx](https://github.com/uber-go/fx) 的显式应用级
依赖图、[Kratos](https://github.com/go-kratos/kratos) 的传输/错误边界、
[go-zero](https://github.com/zeromicro/go-zero) 的生成式服务上下文。gin-bear 不在本轮复制
它们的完整组件生态。

## 9. 实施阶段

实施按以下四个可独立验证阶段串行进行：

1. **请求安全与响应**：终止语义、严格 Fairing、URI 权威、预编码、StatusResponse。
2. **IoC 与生命周期**：严格依赖、错误式构建、发现顺序、失败回滚、可续 Stop、Serve 状态机。
3. **v0.9.2 运行时安全**：生产配置、WebSocket 配额、JWT、Redis Context、Casbin。
4. **v0.9.3 脚手架与授权**：Authorizer、manifest、自动注册、生成事务、CRUD、dialect、文档版本。

每个阶段严格执行红灯测试、最小实现、目标包测试、交叉审查；前一阶段通过后才进入下一阶段。
不同阶段不得同时修改同一核心文件。第 1 至 3 阶段组成 `v0.9.2` 独立实施计划和本地候选
检查点；其完整门禁通过后才编写并执行第 4 阶段的 `v0.9.3` 实施计划。这样保持一次任务连续
完成，但避免一个提交或一个补丁版本横跨全部子系统。

## 10. 验证门禁

提交完成前至少运行：

```text
go test ./...
go test -shuffle=on -count=20 ./pkg/bear ./internal/cli ./internal/scaffold
go test -race -count=3 ./pkg/bear ./internal/cli ./internal/scaffold
go vet ./...
staticcheck ./...
govulncheck ./...
make verify-release
make verify-rc
```

还必须满足：

- 新增缺陷先看到对应测试失败，再修改实现；
- Fairing、IoC、Lifecycle、WebSocket、JWT、Casbin 和 scaffold 有定向回归测试；
- 两个 Bear 的隔离、并发 ApplyAll/Serve/Stop 和 Context 超时有 race 测试；
- `go test -mod=readonly` 的新项目 E2E 通过；
- `v0.9.2` 的 `apidiff` 对 `v0.9.1`、`v0.9.3` 对本地 `v0.9.2` 快照均只报告兼容新增；
- 不新增容器或 K8s 文件；不生成 release tag；不 push。

最终结论分别报告代码测试、脚手架真实生成、依赖/漏洞审计和发布状态，不能把“本地通过”
表述为“已经发布”。

## 11. 明确不做

- 不实现 Docker、Kubernetes、Helm 或云平台模板。
- 不重写已经稳定的数据仓储、迁移引擎、OpenAPI、Tracing、Metrics 和插件加载器。
- 不承诺 Go `.so` 真正热卸载。
- 不在本轮删除全局兼容 facade 或旧 API。
- 不实现策略存储、管理后台或特定业务 RBAC 模型；只提供资源权限契约和接入点。
- 不实现自动升级时对用户 Go 源码的三方合并。

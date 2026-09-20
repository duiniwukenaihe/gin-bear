# OpenCode 第二次复验（2026-09-20）

本节覆盖下方历史审查结论。当前 HEAD 仍为 e7959cc，验证对象是未提交工作区。结论：修复有明显进展，已有测试均通过，但补充边界测试仍复现 3 个问题，尚不能验收运行时 Agent/写任务生产能力。

## 本轮实际执行结果

工具链 Go 1.25.14；共享 GOCACHE/GOMODCACHE；任务级 GOTMPDIR/TMPDIR 位于仓库外，构建串行。开始约 28 GiB 可用，结束约 27 GiB。未清理其他任务的缓存或产物。

| 检查 | 本轮结果 |
| --- | --- |
| 根仓库 `go test ./... -count=1` | PASS |
| tools/bear-mcp `go test ./... -count=1` | PASS |
| extensions/agent `go test -race ./... -count=1` | PASS，含 tasks/example/evals |
| pkg/bear CasbinAuthorizer/RepositoryRequestContext 定向 race | PASS |
| git diff --check | PASS |
| TestReviewDefaultWriteCrash（临时补充测试） | FAIL：外部副作用执行两次 |
| TestReviewInterleavedToolStream（临时补充测试） | FAIL：两次工具调用被拆成四次 |
| TestReviewJSONSchemaContract（临时补充测试） | FAIL：缺必填字段被接受，供应商参数定义丢失 |
| 完整 make verify、发布模式 E2E、真实 PG/MySQL/Redis、真实模型、GitHub CI | 本轮未执行，不能由普通 go test 的包级 PASS 推断通过 |

补充测试通过 Go -overlay 注入仓库外的测试副本，使用实际业务源码与现有测试辅助函数。没有改动/提交业务或测试源码；临时文件已清理。

## 当前未关闭问题

### V1 / P1：默认 Worker 仍可能重复执行已成功写操作（原 R2 未完整修复）

位置：`extensions/agent/tasks/tasks.go:711`；Recover 的 reserved_tokens 条件约在 528 行。

写入意图仅在 WriteReserveTokens > 0 时记录。默认零值仍执行写操作，却不设置 ReservedTokens/ExecNonce；Recover 把这种过期 running write 当作可重试任务。

实际复现：提交并审批写任务→使用未设置 WriteReserveTokens 的 Worker→Executor 增加一次外部副作用计数→BeforeStore 注入崩溃→过期租约并 Recover→再次 RunOnce。结果：`duplicate external effect: count=2 recovered_status=queued second_run_error=<nil>`。

修复要求：所有 write 在执行前必须持久化意图，不应把正确性绑定到可选预算参数；未配置保护时明确拒绝写任务也可。恢复以执行意图状态决定 unknown/对账。新增默认值与显式配置两类回归，均不得自动重复执行。

### V2 / P2：交错多工具流会丢失调用 ID 与完整参数（原 R6 的边界回归）

位置：`extensions/agent/openai.go:362`。

pumpSSE 在 index 改变时立即 flush 并删除上一个工具的缓存。合法流可交错返回 index 0/1，再返回 0/1 的后续 arguments；后续片段一般不再含 id/name，因此被当成新的空 ID 调用。

实际复现：第一帧含调用 a/b 各自参数开头 `{`，第二帧各自补齐 `"x":1}` 和 `"x":2}`。collectStream 得到 4 个调用，而非两个完整调用，包含空 ID 和不完整 JSON 参数。

修复要求：按 index 独立累积，保留首帧 ID/name，直到该轮完成才输出完整工具调用；文本增量继续实时转发。测试至少覆盖交错两工具、单帧两工具、后续片段不带 ID、异常提前断流。

### V3 / P2：JSONSchema 形式的工具绕过参数校验（原 R10 未完整修复）

位置：`extensions/agent/schema.go:29`、`:53`。

declaredParams 只读 ToolInfo 的 Params 字段，不读 Eino 同样支持的 NewParamsOneOfByJSONSchema。后者因此被视为“未声明参数”，validateToolArgs 直接成功，vendorParameters 也返回 nil。

实际复现：把现有 declaredTool 的 ParamsOneOf.ToJSONSchema() 转成 NewParamsOneOfByJSONSchema；传空参数对象。结果：缺少必填 order_id 仍通过，发给供应商的参数定义为空。

修复要求：使用 ParamsOneOf.ToJSONSchema() 统一两种声明，执行侧验证与供应商序列化共享同一完整契约。暂不支持的 schema 必须在注册时明确拒绝，不能静默降级。补两种声明等价、嵌套对象、数组元素约束回归。

## 上轮问题复核状态

- R1：Claim/Complete 已改条件更新；现有跨实例、恢复测试及 race 通过。此结论不替代真实 PostgreSQL 并发验收，也不代表所有审批/对账状态转换都已审计。
- R2：仍有 V1，不能关闭。
- R3：原路径问题描述的是 ci.yml 切子模块工作目录的旧写法；2026-09-20 核实当前 ci.yml 已无子模块 job，不存在 nested.yml（远端 CI 按用户决策跳过）。
- R4：已增加调用前预算检查和供应商输出上限，测试通过；输入估算与缺 usage 固定预留仍属近似计量，不可宣称硬费用上限。
- R5：Runner 绑定可信身份，HTTP 隔离测试通过。
- R6：已增量解析和逐批刷新，但多工具交错仍有 V2。
- R7：相对显式配置能够重放，新增回归通过；绝对 --config 输入在 apply 被拒绝，尚需明确与生成命令的契约一致性。
- R8：所有 kind 统一计算依赖 pins，相关测试通过。
- R9：跨平台缓存默认值与磁盘门槛已补；Linux 实跑不在本轮证据内。
- R10：基础 Params 声明已补，但 JSONSchema 路径仍有 V3。
- Casbin 上次两项回归和定向 race 通过；生成 Service/Repository 回归通过，但仍未经过生成 Controller 的 HTTP 入口。
- MCP 二进制现已由根 .gitignore 精确忽略；agent.md/AGENTS.md 继续忽略。未删除其他工具创建的二进制。

建议先关闭 V1，再修 V2/V3；保持写任务生产入口关闭。修后把临时复现改成正式回归，再执行对应模块测试及完整发布门禁，并单独报告真实依赖、容器和模型验证。

---

以下保留首次审查历史，不代表本轮未修复清单。

# OpenCode 实现复核（2026-09-20）

结论：不建议把 W0—W11 标为全部验收完成。上次 Casbin 的两个缺陷已在源码中修正，并添加对应回归；新增生成测试确实生成 Service/Repository，但仍没有经过 Controller 的 HTTP 入口。新增运行时与持久任务存在阻断问题。

## 本次验证边界

- 审查工作区（HEAD 仍为 e7959cc，新增实现尚未提交），未修改业务代码。
- 磁盘可用约 9.9 GiB，低于本地 10 GiB 门槛，因此未启动 Go 编译、全量测试、race、容器或真实模型调用。不能把上一轮绿色结果用于这批新增代码。
- 已执行 git diff --check，通过；Agent 文件仍被忽略。
- 用 Python 标准库 SQLite，在内存数据库中使用 tasks 的实际建表 SQL 和等价的 SELECT/UPDATE 顺序复核跨实例交错：两个 worker 均得到 task/version=1，旧 worker 的 version 仍匹配数据库；Recover 后 write 状态变 queued，原 approval 保留。这是 SQL 级复现，非 Go Worker/真实 PostgreSQL 端到端复现。
- CI 的两个 working-directory 下均不存在 scripts/ci-diagnostic.sh，文件系统核查为 false。

## 阻断与修复建议

### R1 / P1：任务领取和完成不具备跨实例 fencing

位置：`extensions/agent/tasks/tasks.go:345`、`:573`。

Claim 先 SELECT 再调用 update；update 只按 id 更新。Store.mu 仅约束同一个 Store 对象，两个进程/Store 可以同时读到 queued/version=0，然后都写 running/version=1 并执行。Complete 也是先读版本、后无条件 UPDATE，无法原子拒绝过期 worker，也可能覆盖并发取消。内存 SQLite SQL 交错已经证明版本碰撞。

修复：领取采用事务行锁或带 status/version 条件的原子 CAS，检查 RowsAffected；完成以版本、运行状态及租约所有者作为 UPDATE 条件。对两个独立 Store/连接测试同时领取、完成与取消、租约恢复与迟到完成，真实 PostgreSQL 必测。

### R2 / P1：恢复会盲重试可能已经执行的写操作

位置：`extensions/agent/tasks/tasks.go:453`、Worker.RunOnce。

Recover 不区分 readonly/write，所有过期 running 都变 queued。若外部写已成功、Complete 前进程崩溃，旧审批仍可通过，新的 worker 会再次执行；只有 Executor 正常返回 ErrUnknownResult 才进入 unknown，真正崩溃不会走该返回路径。崩溃前实际消耗的预算也不会被 Complete 扣除。

修复：写操作在提交外部副作用前持久化执行意图和幂等标识；恢复未确认写入进入 unknown/对账，不直接重新执行。先用假的外部副作用计数器验证“外部成功→落库前崩溃→恢复”只发生一次或明确待对账。预算预留也必须跨崩溃保存。

### R3 / P1：嵌套模块 CI 在运行测试前就会失败

位置：`.github/workflows/ci.yml:100`、`:106`。

working-directory 分别为 tools/bear-mcp、extensions/agent，但命令使用根目录相对路径 scripts/ci-diagnostic.sh。这两个目录没有该脚本。

修复：使用仓库根绝对路径（GITHUB_WORKSPACE）调用脚本，或正确的 ../../scripts 路径，保留模块工作目录。验收必须得到 GitHub 上对应 job 的实际成功结果。

### R4 / P1：预算仅事后记账，不能限制供应商生成量

位置：`extensions/agent/runner.go:99`、`extensions/agent/openai.go:122`、`extensions/agent/tools.go:197`。

调用模型前不根据剩余预算预留/拒绝请求，只在完整响应后 chargeTokens；OpenAI request 声明 MaxTokens 但两条请求路径均未设置，也没有把调用选项映射过去。即使 MaxTokens 很小，供应商仍可能执行一次远超上限的生成并产生费用。流式 usage 未请求/保留完整报告，会退回固定 ReserveTokens，不能视为实际费用上限。

修复：定义预算口径，调用前预留与检查输入/输出预算，向供应商传受限输出参数，响应后对账；缺 usage 时保守处理，无法计量时不得宣称硬费用保证。用假 HTTP 服务断言发送的上限、预算耗尽后不再发起请求。

### R5 / P2：HTTP 身份没有传给业务工具

位置：`extensions/agent/http.go:403`、`extensions/agent/runner.go:117`、`extensions/agent/example/example.go`。

SetIdentity 只写 gin.Context；Runner 把 identity 参数用于 Authorize，却没有用 WithIdentity 放进 Execute 的原生 context。示例 GetOrderTool.Execute 从 IdentityFromContext 取身份，正常 HTTP 接入会返回 missing identity。现有 example 测试手工 WithIdentity，绕过了这个断点。

修复：在可信入口统一绑定原生请求/运行 context，确保 Authorize 与 Execute 使用同一身份。增加 IdentityFairing→Handler→Runner→真实订单工具的 HTTP 测试，验证同租户成功、跨租户拒绝。

### R6 / P2：所谓流式响应会等待完整供应商结果

位置：`extensions/agent/openai.go:280`、`extensions/agent/http.go:186`。

readSSEChunks 先 ReadAll 后解析，再返回数组 StreamReader，因此首个 token 必须等待上游结束。Handler 的 flush 只执行一次，后续事件会被 HTTP 缓冲；streamWriteTimeout 也没有设到 socket 写期限，select 超时不能打断阻塞的 Fprintf。

修复：增量解析 SSE，收到帧即传递，正确终止/关闭；每批事件刷新，使用响应写 deadline 并处理写错误。用真实 httptest.Server 和分段供应商验证“上游未完成时客户端已收到首帧”，再测慢客户端取消与 goroutine 退出。

### R7 / P2：显式配置生成的预览无法正确 apply

位置：`internal/cli/apply.go:65`。

预览支持 --config，apply 却固定 resolveGeneratedAPIDatabase(directory, nil)，resourceOptions 也不恢复 ConfigPaths；因此非默认配置生成的计划会被当成 stale，或用默认链重新解析，用户无法完成批准计划。

修复：计划分别记录显式配置输入与默认发现方式；apply 校验路径后重放同一选择规则，核对内容摘要。增加 prod/base 方言不同且显式指定配置的 preview→apply 回归。

### R8 / P2：model/dto 的 decimal 依赖自动补齐发生回归

位置：`internal/cli/plan.go` 中仅 opts.Kind == "api" 分支调用 computePins。

computePins 本身支持所有 kind 的 decimal 字段，但新 plan 流程仅为 api 计算 pins。新生成 model/dto 使用 decimal.Decimal 时不会再补 go.mod 依赖，而旧流程会对各 kind 执行依赖补齐。

修复：按 kind/fields 对所有类型计算必要 pins，仍只对 api 规划数据库迁移。分别生成 model/dto decimal 字段并编译实际项目。

### R9 / P2：集成脚本默认使用本机绝对缓存路径

位置：`scripts/test-integration.sh:31`。

未设置 GOCACHE/GOMODCACHE 的 Linux runner 会被强制指向 /Users/zhangpeng/...；普通 runner 通常无权创建 /Users，不能作为跨平台门禁。脚本也未在构建前执行磁盘检查。

修复：非本机使用 go env 的用户默认缓存；Mac 特例不能传播到 CI。补无预设缓存环境变量的 Linux 验收、磁盘门槛与本任务临时目录退出清理。

### R10 / P2：供应商工具定义缺失参数 schema

位置：`extensions/agent/openai.go` 的 Generate/Stream tools 转换。

ToolInfo.ParamsOneOf 定义了参数，但请求只填 Name/Description，没有填 openAIFunction.Parameters。真实供应商收到的工具缺少参数结构，严格端点可能拒绝；宽松端点也无法可靠遵循必填参数。decodeArgs 仅解到 map，不校验该 schema，声明“严格参数结构”尚未落实。

修复：两条路径共用 schema 转换；执行前校验参数类型、必填字段和额外字段策略。线协议测试断言完整参数定义，而不是仅检查工具名称。

## 其他交付问题

- `tools/bear-mcp/bear-mcp` 为 10,965,138 字节的未忽略二进制，当前未提交；应移到仓库外并添加精确忽略规则，不能随 tools/ 一起提交。本轮未删除他人工件。
- 生成取消回归目前真实经过 Service/Repository，但没有执行生成 Controller；应补 HTTP 路由入口覆盖，或收窄“整链已验证”描述。
- W0 总台账仍 NOT_STARTED，子项 DONE；W1/W2/W7—W10 含 MySQL、容器、真实模型 NOT_RUN 却总标 DONE。建议拆成代码完成/本地测试/真实依赖/真实模型/部署验收，不沿用同一个 DONE。
- 本次没有复核实时漏洞库或线上服务；上述结论不等同于完成全部安全审计。

## 建议收尾顺序

先修 R1/R2，写操作保持关闭；修 R3/R9 恢复可执行门禁；修 R4/R5/R6/R10 后验收只读 Agent；修 R7/R8 再验证生成 CLI。恢复至少 10 GiB 可用磁盘后，串行运行上次 Casbin 回归、生成链路、根完整门禁、两个嵌套模块及 race，再分别进行真实数据库和容器验收。清理磁盘需按已授权边界处理，不能删除不明归属文件。

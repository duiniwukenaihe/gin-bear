# gin-bear v0.9.2 Runtime Hardening Implementation Plan

> **For agentic workers:** Execute one task at a time. Every behavior change starts with a failing test, then the minimal implementation, targeted verification, review, and a Chinese commit.

**Goal:** Deliver the `v0.9.2` request-pipeline, IoC/lifecycle, serving-state, and runtime-security candidate without changing existing public signatures or adding fields to existing exported configuration structs.

**Architecture:** New behavior is enabled through accessors over `SysConfig.Config`; existing applications remain in compatibility mode unless `framework.strict` is true. Security boundary repairs apply in both modes. Core changes stay application-scoped through `Bear.Runtime()` and avoid the legacy process-wide facade.

**Tech Stack:** Go 1.25.12, Gin, Gorilla WebSocket, golang-jwt/v5, go-redis/v9, Casbin, standard `net/http`, `encoding/json`, `reflect`, `sync`, and `context`.

## Global Constraints

- Baseline commit is `4dbc3d12ece9858976c6c28e0cea54235a81b8bd`; do not reimplement completed baseline features.
- Work only on `codex/v09x-framework-hardening`; do not push, tag, merge `main`, or modify the detached worktree.
- Preserve `v0.9.1` source compatibility: no removed or changed public signatures and no fields added to existing exported structs.
- Existing apps default to compatibility mode and raw responses; new security termination behavior applies in both modes.
- Use `GOSUMDB=sum.golang.org GOTOOLCHAIN=go1.25.12` for Go commands.
- No Docker, Compose, Kubernetes, or deployment manifests.
- Commit titles and bodies are Chinese and describe tests plus unpublished status.

---

### Task 1: Runtime Contract Accessors and Error-Returning Ignite

**Files:**
- Modify: `pkg/bear/config.go`
- Modify: `pkg/bear/config_loader.go`
- Modify: `pkg/bear/bear.go`
- Test: `pkg/bear/config_loader_test.go`
- Test: `pkg/bear/production_baseline_test.go`

**Interfaces:**
- Produces `(*SysConfig).FrameworkStrict() bool`.
- Produces `(*SysConfig).SetFrameworkStrict(bool)`.
- Produces `(*SysConfig).ResponseMode() string` and `(*SysConfig).SetResponseMode(string) error`.
- Produces `IgniteE(args ...any) (*Bear, error)`; legacy `Ignite` remains a panic wrapper.

- [ ] Add failing tests `TestFrameworkRuntimeContractDefaults`, `TestFrameworkRuntimeContractReadsExtensionKeys`, `TestSetResponseModeRejectsUnknownMode`, and `TestIgniteEReturnsValidationError`.

```go
func TestFrameworkRuntimeContractDefaults(t *testing.T) {
	cfg := NewSysConfig()
	if cfg.FrameworkStrict() || cfg.ResponseMode() != "raw" {
		t.Fatalf("defaults = strict:%v mode:%q", cfg.FrameworkStrict(), cfg.ResponseMode())
	}
}
```

- [ ] Run `go test ./pkg/bear -run 'TestFrameworkRuntimeContract|TestSetResponseMode|TestIgniteE' -count=1`; expect compile failures for the new API.
- [ ] Implement extension-key parsing with constants `framework.strict` and `framework.response_mode`; initialize `Config` before setters write; reject modes other than `raw`/`envelope` in the setter and `SysConfig.Validate`.
- [ ] Move Ignite construction into `IgniteE`; return load, validation, database, production-security, trusted-proxy, and engine-construction errors. Keep `Ignite` as:

```go
func Ignite(args ...any) *Bear {
	app, err := IgniteE(args...)
	if err != nil { panic(err) }
	return app
}
```

- [ ] Run the targeted command, then `go test ./pkg/bear -run 'TestFrameworkRuntimeContract|TestSetResponseMode|TestIgniteE|TestProduction' -count=1`; expect PASS.
- [ ] Commit with title `feat: 增加严格运行契约与错误式启动入口` and a Chinese body listing accessors, compatibility defaults, tests, and unpublished status.

### Task 2: Authoritative Binding and Buffered Response Writing

**Files:**
- Modify: `pkg/bear/binding.go`
- Modify: `pkg/bear/responder.go`
- Modify: `pkg/bear/bear.go`
- Modify: `pkg/bear/http_error.go`
- Modify: `pkg/bear/runtime.go`
- Test: `pkg/bear/handler_test.go`
- Test: `pkg/bear/error_contract_test.go`

**Interfaces:**
- Produces `type StatusResponse struct { Status int; Value any }` and `WithStatus(int, any) StatusResponse`.
- Produces internal `writeSuccessWithConfig(*gin.Context, *SysConfig, any)`.
- Consumes Task 1 `ResponseMode()`.

- [ ] Add failing tests proving JSON/query cannot override a `uri` field, JSON marshal failure returns one 500 instead of 200, `WithStatus(201, value)` writes 201, envelope mode wraps ordinary values, 204/304/HEAD write no body, and `WriteError` logs but does not append after a committed response.

```go
type authoritativeIDRequest struct {
	ID int64 `uri:"id" json:"id" form:"id"`
}
```

- [ ] Run `go test ./pkg/bear -run 'TestURIValueWins|TestBufferedSuccess|TestStatusResponse|TestCommittedError' -count=1`; expect failures showing body/query overwrite or missing APIs.
- [ ] Change aggregate binding order to query, form/JSON, URI, validator. Preserve scalar path binding and stable path errors.
- [ ] Encode the complete response with `encoding/json.Marshal` before setting headers or status. Validate status range 200..599; skip bodies for 204, 304, and HEAD. In envelope mode wrap ordinary values as `Response{Code: status, Message: ..., Data: value}`.
- [ ] Make `WriteError` call `logHTTPError` before the written check; make recovery log and Abort without appending when the writer is committed.
- [ ] Run targeted tests and `go test ./pkg/bear -run 'Test.*(Binding|Response|Error|Recovery)' -count=1`; expect PASS.
- [ ] Commit with title `fix: 统一绑定权威值与响应提交语义`.

### Task 3: Strict Fairing Stack and Universal Terminal Semantics

**Files:**
- Modify: `pkg/bear/fairing.go`
- Modify: `pkg/bear/bear.go`
- Test: `pkg/bear/handler_test.go`
- Test: `pkg/bear/ioc_controller_injection_test.go`
- Test: `pkg/bear/production_baseline_test.go`

**Interfaces:**
- Produces `(*FairingHandler).OnResponseE(any) (any, error)`.
- Consumes Task 1 `FrameworkStrict()` and Task 2 response writer.

- [ ] Add failing tests for strict request order `global,controller,route,handler`, reverse response order `route,controller,global`, Controller Fairing that only writes 403 without Abort, and WebSocket Fairing that writes 401 before Upgrade.
- [ ] Run `go test ./pkg/bear -run 'TestStrictFairing|TestControllerFairingTerminal|TestWebSocketFairingTerminal' -count=1`; expect order/continuation failures.
- [ ] Add a per-request entered-Fairing stack. In strict mode run global, controller, route before the handler and unwind only entered Fairings in reverse. In compatibility mode preserve historical order while applying terminal checks before and after every OnRequest.
- [ ] Replace Controller `group.Use` behavior with a terminal-aware path; avoid registering the same Controller Fairing both as Gin middleware and compiled pipeline in strict mode.
- [ ] Check `requestFairingTerminal` immediately after global WebSocket Fairings and before shutdown checks or Upgrade.
- [ ] Implement `OnResponseE`; keep legacy `OnResponse` swallowing errors exactly as before.
- [ ] Run targeted tests and `go test ./pkg/bear -run 'Test.*Fairing|Test.*Interceptor|Test.*WebSocket' -count=1`; expect PASS.
- [ ] Commit with title `fix: 完整终止并反向展开严格拦截链`.

### Task 4: Strict IoC Resolution and Registration Diagnostics

**Files:**
- Modify: `pkg/bear/ioc.go`
- Modify: `pkg/bear/runtime.go`
- Modify: `pkg/bear/bear.go`
- Test: `pkg/bear/ioc_controller_injection_test.go`
- Create: `pkg/bear/ioc_strict_test.go`

**Interfaces:**
- Produces `ResolveE[T any](*BeanFactory) (T, error)`.
- Produces `(*BeanFactory).ApplyE(any) error`, `(*Bear).BeansE(...Bean) error`, `(*Bear).AddModuleE(...Module) error`, and `(*Bear).MountE(string, ...IClass) error`.
- Produces exported sentinel errors `ErrBeanMissing`, `ErrBeanAmbiguous`, and `ErrBeanDuplicate` usable through `errors.Is`.

- [ ] Add failing tests for a missing inject field, two implicit implementations of one interface, two different instances of one concrete type, explicit interface binding precedence, same-instance idempotence, and two packages with equal struct names using full static-injector keys.
- [ ] Run `go test ./pkg/bear -run 'TestStrictIOC|TestResolveE|TestApplyE|TestStaticInjectorKey' -count=1`; expect missing API or silent-resolution failures.
- [ ] Track concrete registration conflicts without changing compatibility Set behavior. Make exact explicit interface keys win; return ambiguity for multiple implicit candidates.
- [ ] Add error-returning runtime static injectors keyed by `reflect.Type.PkgPath()+"."+Name()`. Strict ApplyE uses the E injector or container-local reflection, never the process facade.
- [ ] Make strict Bear registration methods propagate errors; legacy methods remain wrappers with current signatures.
- [ ] Run targeted tests and `go test ./pkg/bear -run 'Test.*(IOC|Inject|Bean|Container)' -count=1`; expect PASS.
- [ ] Commit with title `feat: 增加可诊断的严格依赖注入`.

### Task 5: Error-Returning Build and Complete Strict Discovery

**Files:**
- Modify: `pkg/bear/bear.go`
- Modify: `pkg/bear/plugin.go`
- Test: `pkg/bear/lifecycle_hardening_test.go`
- Create: `pkg/bear/build_strict_test.go`

**Interfaces:**
- Produces `ModuleBuilderE` and `ClassBuilderE`, each with `BuildE(*Bear) error`.
- Produces `ErrBuildRegistrationLoop`.
- Consumes Task 4 strict registration/injection APIs.

- [ ] Add failing tests proving Module Build-discovered controllers receive injection and Init before serving, BuildE errors are returned, route building occurs once, registration converges within 32 rounds, round 33 returns `ErrBuildRegistrationLoop`, and compatibility mode retains Init-before-Build order.
- [ ] Run `go test ./pkg/bear -run 'TestStrictBuild|TestBuildE|TestCompatibilityBuildOrder' -count=1`; expect lifecycle/order failures.
- [ ] Split ApplyAll into explicit build and init phases. Strict mode: register/inject module, BuildE/Build, discover mounts, inject controller/Fairing, BuildE/Build routes, repeat newly discovered beans up to 32 rounds, validate all dependencies, seal, then Init.
- [ ] Mark route build complete independently from lifecycle start so a retryable Init failure cannot duplicate routes. Treat Build errors/panics as terminal apply failures.
- [ ] Preserve the compatibility path’s existing inject, Init, Module Build, Controller Build order.
- [ ] Run targeted tests and `go test ./pkg/bear -run 'Test.*(ApplyAll|Build|Module|Mount)' -count=1`; expect PASS.
- [ ] Commit with title `feat: 完成严格构建发现与错误返回链路`.

### Task 6: Retryable Strict Lifecycle and Resumable Stop

**Files:**
- Modify: `pkg/bear/lifecycle.go`
- Modify: `pkg/bear/runtime.go`
- Modify: `pkg/bear/bear.go`
- Test: `pkg/bear/lifecycle_hardening_test.go`
- Test: `pkg/bear/lifecycle_review_fix_test.go`

**Interfaces:**
- Produces private strict entry states `pending`, `stopping`, `retryPending`, `stopped`, `stoppedWithError`.
- Consumes Task 1 strict mode and Task 5 split build/init state.

- [ ] Add failing tests for cleanup of the failing Initializer itself, LIFO cleanup, retry after complete cleanup, no retry after rollback failure, ContextShutdowner retry with a fresh Context, and legacy Shutdowner continuing in one worker after caller timeout.
- [ ] Run `go test ./pkg/bear -run 'TestStrictLifecycle|TestResumableStop|TestFailingInitializerCleanup' -count=1`; expect failures.
- [ ] Add `newLifecycleWithMode(strict bool)` while preserving `newLifecycle()` compatibility. Strict Start marks the current entry cleanup-required before Init and checks Context before every component.
- [ ] For legacy Shutdowner, retain one worker and completion channel. For ContextShutdowner, map context errors to retryPending, nil to stopped, and non-context errors to stoppedWithError. Continue reverse pending work on later Stop calls.
- [ ] Make strict ApplyAll retry Init only after complete successful rollback; preserve cached errors in compatibility mode.
- [ ] Run targeted tests and `go test ./pkg/bear -run 'Test.*(Lifecycle|Shutdown|Rollback|ApplyAll)' -count=1`; expect PASS.
- [ ] Commit with title `fix: 支持严格生命周期回滚重试与续关`.

### Task 7: Signal-Free Serve and Single Serving Owner

**Files:**
- Modify: `pkg/bear/bear.go`
- Modify: `pkg/bear/runtime.go`
- Test: `pkg/bear/lifecycle_test.go`
- Test: `pkg/bear/lifecycle_review2_test.go`

**Interfaces:**
- Produces `(*Bear).Serve(context.Context) error` and `ErrAlreadyServing`.
- Keeps `Launch(context.Context) error` as the signal-aware compatibility wrapper.

- [ ] Add failing tests for direct Serve startup/shutdown, concurrent ApplyAll and Serve sharing one initialization, a second Serve returning `ErrAlreadyServing`, a second Launch not stopping the first, and strict conflicting Gin modes returning `ErrGinRuntimeConflict` from IgniteE.
- [ ] Run `go test ./pkg/bear -run 'TestServe|TestConcurrentApplyAndServe|TestGinRuntimeConflict' -count=1`; expect missing API/race failures.
- [ ] Move listener, serving, and shutdown ownership from Launch into Serve. Serve calls/waits for ApplyAll and uses one atomic/mutex-protected owner state. A rejected caller never invokes Lifecycle.Stop.
- [ ] Make Launch create the SIGINT/SIGTERM child Context and delegate to Serve. Register the first strict Gin mode process-wide; allow equal mode and reject a conflicting strict mode before changing Gin globals.
- [ ] Run targeted tests, then `go test -race ./pkg/bear -run 'TestServe|TestConcurrentApplyAndServe|TestGinRuntimeConflict' -count=3`; expect PASS.
- [ ] Commit with title `feat: 增加无信号服务入口与单一运行所有权`.

### Task 8: Production Proxy and WebSocket Resource Boundaries

**Files:**
- Modify: `pkg/bear/config.go`
- Modify: `pkg/bear/websocket.go`
- Modify: `pkg/bear/runtime.go`
- Modify: `pkg/bear/bear.go`
- Test: `pkg/bear/lifecycle_hardening_test.go`
- Test: `pkg/bear/production_baseline_test.go`
- Test: `pkg/bear/task10_coverage_test.go`

**Interfaces:**
- Consumes extension key `websocket.max_connections` and existing WebSocket policy keys.
- Produces private Runtime slot acquire/release functions and current-Bear WebSocket route count.

- [ ] Add failing table tests for wildcard production Origin, normalized `/0` trusted proxies, all documented timeout/message/connection boundaries, ping >= read timeout, strict WebSocket route without allowlist, and a second connection rejected at a cap of one.
- [ ] Run `go test ./pkg/bear -run 'TestProductionRejects.*(WebSocket|TrustedProxy)|TestWebSocket.*Limit' -count=1`; expect acceptance of unsafe config or missing cap.
- [ ] Parse trusted proxies with `net/netip` and reject any prefix with zero bits. Enforce fixed WebSocket defaults/ranges from the design. Perform route-dependent allowlist validation after strict route build and before Init.
- [ ] Acquire a per-Runtime connection slot before Upgrade; release on Upgrade failure and deferred connection close. Return one 503 JSON/error response without Upgrade when full.
- [ ] Run targeted tests and `go test -race ./pkg/bear -run 'Test.*WebSocket|Test.*TrustedProxy' -count=3`; expect PASS.
- [ ] Commit with title `fix: 收紧生产代理与 WebSocket 资源边界`.

### Task 9: Bounded JWT, Request Context, and Casbin Isolation

**Files:**
- Modify: `pkg/bear/jwt.go`
- Modify: `pkg/bear/auth_token.go`
- Modify: `pkg/bear/jwt_fairing.go`
- Modify: `pkg/bear/casbin.go`
- Test: `pkg/bear/security_auth_test.go`
- Test: `pkg/bear/config_loader_test.go`
- Create: `pkg/bear/casbin_isolation_test.go`

**Interfaces:**
- Produces `(*AuthTokenManager).ParseTokenContext(context.Context, string) (*CustomClaims, error)`.
- Keeps `ParseToken(string)` as a Background compatibility wrapper.
- Uses fixed `maxJWTTokenBytes = 16 << 10`.

- [ ] Add failing tests for a token over 16 KiB, one-pass valid-method parsing, canceled request Context reaching Redis, AuthFairing using request Context, two Bear instances with different Casbin enforcers, no global fallback, and generic public 500 on Enforce errors.
- [ ] Run `go test ./pkg/bear -run 'TestJWTTokenSize|TestParseTokenContext|TestCasbin.*Isolation|TestCasbin.*Error' -count=1`; expect failures.
- [ ] Reject oversized tokens before JWT parsing. Remove ParseUnverified and configure one `ParseWithClaims` call with valid methods, expiration, leeway, issuer, and audience.
- [ ] Implement ParseTokenContext and pass `ctx.Request.Context()` from AuthFairing to Redis blacklist checks. Preserve fail-closed behavior.
- [ ] Remove Casbin `GetByType` fallback; require current-container injection, log internal errors, and return a generic client 500.
- [ ] Run targeted tests and `go test -race ./pkg/bear -run 'Test.*(JWT|Token|AuthFairing|Casbin)' -count=3`; expect PASS.
- [ ] Commit with title `fix: 隔离认证授权并传播请求上下文`.

### Task 10: v0.9.2 Documentation, Compatibility, and Full Candidate Gate

**Files:**
- Modify: `README.md`
- Modify: `CHANGELOG.md`
- Modify: `docs/production.md`
- Modify: `docs/runbook.md`
- Modify: `docs/migration-v0.9-to-v0.10.md`
- Modify: `docs/compatibility.md`
- Modify: `internal/cli/new.go`
- Modify tests under: `scripts/`
- Modify tests under: `internal/scaffold/`

**Interfaces:**
- Documents `framework.strict`, `framework.response_mode`, IgniteE, Serve, strict migration, Casbin injection, JWT cap, and WebSocket limits.
- Changes only the unpublished candidate label from `v0.10.0-rc.1` to `v0.9.2`.

- [ ] Add/update documentation tests first so stale `v0.10.0-rc.1` references and missing migration guidance fail.
- [ ] Run `go test ./scripts ./internal/scaffold ./internal/cli -run 'Test.*(Release|Documentation|Version|Scaffold)' -count=1`; expect failures listing stale candidate text.
- [ ] Update docs and CLI fallback to unpublished `v0.9.2`; include compatibility defaults, strict opt-in, forced security changes, and no-publish status. Do not modify the earlier historical specs.
- [ ] Run focused documentation tests; expect PASS.
- [ ] Run the complete candidate gate:

```bash
go test ./...
go test -shuffle=on -count=20 ./pkg/bear ./internal/cli ./internal/scaffold
go test -race -count=3 ./pkg/bear ./internal/cli ./internal/scaffold
go vet ./...
staticcheck ./...
govulncheck ./...
make verify
make verify-rc
```

- [ ] Run the controlled API compatibility check against `v0.9.1`; expect no incompatible output. Confirm `git status --short` contains only intentional files and no coverage, binary, temp, container, or Kubernetes artifacts.
- [ ] Commit with title `docs: 完成 v0.9.2 候选说明与验证门禁` and a Chinese body containing every command outcome, API compatibility result, and “未推送、未打标签、未发布”.

## Completion Boundary

After Task 10, record the exact `v0.9.2` local checkpoint commit and gate evidence. Do not tag or push. Only then create the separate `v0.9.3` implementation plan for Authorizer and scaffold closure, continuing in the same branch as approved.

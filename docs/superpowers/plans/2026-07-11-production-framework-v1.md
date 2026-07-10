# Gin-Bear Production Framework V1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver a production-focused `v0.10.0-rc.1` of the Gin runtime and scaffolding CLI without breaking existing valid `pkg/bear` applications or adding container/Kubernetes assets.

**Architecture:** Make `Bear` own application-scoped runtime state while legacy package helpers remain compatibility facades. Compile handlers once into a single request/result/error pipeline, coordinate all lifecycle work through bounded contexts, and make the one canonical CLI render versioned embedded templates that are verified end to end.

**Tech Stack:** Go 1.25 module baseline, Gin, GORM, go-redis, slog, OpenTelemetry, Prometheus client, Cobra, SQLite tests, miniredis, GitHub Actions.

## Global Constraints

- Keep module path `github.com/duiniwukenaihe/gin-bear`.
- Do not remove existing exported `pkg/bear` symbols in the v0 line.
- Keep legacy YAML keys parseable; enabled no-op features emit one warning.
- Security fixes may reject unsafe production configuration only with a migration note.
- Do not add Docker, Compose, Kubernetes, Helm, or container scanning assets.
- Use TDD for every behavior change and run race tests after every task.
- Use Chinese commit subjects and include the complete behavior changed in each commit body.
- Pin development tools and dependencies; do not install `@latest` in CI.
- Target release is `v0.10.0-rc.1`; `v1.0.0` is outside this plan.

---

### Task 1: Restore A Green And Reproducible Quality Baseline

**Files:**
- Modify: `scripts/release_check_test.go`
- Modify: `scripts/release-check.sh`
- Modify: `.github/workflows/ci.yml`
- Modify: `Makefile`
- Modify: `go.mod`
- Modify: `go.sum`
- Create: `scripts/check-coverage.sh`

**Interfaces:**
- Produces: `make verify`, the single local and CI quality entry point.
- Produces: a core package coverage floor of 60% for this task and 70% at Task 10.
- Pins: `golang.org/x/vuln/cmd/govulncheck@v1.6.0` and `honnef.co/go/tools/cmd/staticcheck@v0.7.0`.

- [ ] **Step 1: Replace the stale dependency-bot test with quality-gate assertions**

```go
func TestRepositoryDependencyChecksDoNotCreateUpdateBranches(t *testing.T) {
	content, err := os.ReadFile("release-check.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	for _, want := range []string{
		"go mod tidy",
		"govulncheck@v1.6.0",
		"staticcheck@v0.7.0",
		"check-coverage.sh",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("release check missing %q", want)
		}
	}
	if _, err := os.Stat("../.github/dependabot.yml"); !os.IsNotExist(err) {
		t.Fatalf("dependabot config must remain absent: %v", err)
	}
}
```

- [ ] **Step 2: Run the focused test and verify the current script fails it**

Run: `go test ./scripts -run TestRepositoryDependencyChecksDoNotCreateUpdateBranches -count=1`

Expected: FAIL because the release script still installs an unpinned vulnerability tool and does not run static analysis or coverage enforcement.

- [ ] **Step 3: Make quality tools deterministic and add the coverage check**

```bash
#!/usr/bin/env bash
set -euo pipefail

profile="${1:-coverage.out}"
minimum="${COVERAGE_MINIMUM:-60.0}"
actual="$(go tool cover -func="${profile}" | awk '/^total:/ {gsub("%", "", $3); print $3}')"
awk -v actual="${actual}" -v minimum="${minimum}" 'BEGIN {
  if (actual + 0 < minimum + 0) {
    printf "coverage %.1f%% is below %.1f%%\n", actual, minimum
    exit 1
  }
  printf "coverage %.1f%% meets %.1f%%\n", actual, minimum
}'
```

Update `scripts/release-check.sh` to run tests with
`-coverprofile=coverage.out`, invoke the script above, and execute the pinned
tools with `go run <module>@<version>`. Add `permissions: contents: read`, a
workflow concurrency group, and separate `verify` and `race` CI jobs so a
failure names the broken gate.

- [ ] **Step 4: Add tests around currently uncovered low-risk helpers until 60% is real**

Cover `ParseConfig`, `WriteFileAtomic`, `Value`, route-tree matching, and CLI
error paths. Do not exclude packages or statements from the profile.

- [ ] **Step 5: Run the complete baseline**

Run: `make verify`

Expected: build, unit tests, 60% coverage, vet, staticcheck, vulnerability scan,
race tests, module tidiness, and `git diff --check` all pass.

- [ ] **Step 6: Commit**

```bash
git add .github/workflows/ci.yml Makefile go.mod go.sum scripts pkg/bear/*_test.go cmd/bear-cli/cmd/*_test.go
git commit -m "ci: 修复质量基线并固定生产检查工具" -m "移除已删除 Dependabot 配置的残留断言，拆分普通与竞态检查，固定漏洞和静态分析工具版本，并建立真实覆盖率门槛。"
```

### Task 2: Lock The Compatibility Contract And Supported Surface

**Files:**
- Create: `docs/compatibility.md`
- Create: `docs/supported-features.md`
- Create: `pkg/bear/legacy_compat_test.go`
- Modify: `pkg/bear/config.go`
- Modify: `pkg/bear/bear.go`
- Modify: `README.md`

**Interfaces:**
- Preserves: `Ignite`, `GetInjector`, `GetByType`, `Handle`, `Mount`, `ApplyAll`, `Launch`, existing config structs, and existing adapter constructors.
- Produces: one startup warning per enabled compatibility-only feature.

- [ ] **Step 1: Write a compile-time legacy application test**

```go
func TestLegacyV091SurfaceStillCompiles(t *testing.T) {
	cfg := NewSysConfig()
	cfg.DB.Enabled = false
	app := Ignite(cfg)
	app.Beans(&legacyService{})
	app.Mount("/api", &legacyController{})
	app.Attach(NewAuthFairing())
	app.EnableHealth().EnableMetrics().EnableGzip()
	if err := app.ApplyAll(context.Background()); err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 2: Verify the test passes before deprecation annotations**

Run: `go test ./pkg/bear -run TestLegacyV091SurfaceStillCompiles -count=1`

Expected: PASS. This becomes the compatibility tripwire for later tasks.

- [ ] **Step 3: Document three feature states and annotate compatibility-only exports**

Use exactly these states in `docs/supported-features.md`:

```text
Supported: HTTP lifecycle, config, IoC, routing/binding/errors, JWT hooks,
GORM, Redis, migrations, cron, WebSocket, health, metrics, tracing, OpenAPI.
Experimental: dynamic Go plugins.
Compatibility-only: gRPC, MQ providers, WAF, GeoIP, BigQuery, schema,
config center, circuit breaker, and ID generator.
```

Add standard Go comments beginning with `Deprecated:` to compatibility-only
methods and types without deleting or moving them.

- [ ] **Step 4: Emit deduplicated warnings for enabled no-op features**

```go
func (c *SysConfig) compatibilityWarnings() []string {
	var warnings []string
	if c.MQ != nil && c.MQ.Enabled {
		warnings = append(warnings, "mq is compatibility-only and is not started")
	}
	if c.ConfigCenter != nil && c.ConfigCenter.Enabled {
		warnings = append(warnings, "config_center is compatibility-only and is not loaded")
	}
	return warnings
}
```

Cover every compatibility-only enabled flag and log the returned strings once
during `Ignite`.

- [ ] **Step 5: Run compatibility and full tests**

Run: `go test ./pkg/bear -run 'TestLegacyV091Surface|TestCompatibilityWarnings' -count=1 && make verify`

Expected: PASS with no public symbol removed.

- [ ] **Step 6: Commit**

```bash
git add README.md docs/compatibility.md docs/supported-features.md pkg/bear/config.go pkg/bear/bear.go pkg/bear/legacy_compat_test.go
git commit -m "docs: 明确框架支持边界和兼容策略" -m "锁定 v0 公开 API，区分正式、实验和仅兼容能力，并为启用但未实现的配置增加一次性启动警告。"
```

### Task 3: Consolidate The CLI And Prove Generated Projects End To End

**Files:**
- Create: `internal/cli/root.go`
- Create: `internal/cli/new.go`
- Create: `internal/cli/gen.go`
- Create: `internal/scaffold/embed.go`
- Create: `internal/scaffold/template/go.mod.tmpl`
- Create: `internal/scaffold/template/cmd/server/main.go.tmpl`
- Create: `internal/scaffold/template/internal/app/app.go.tmpl`
- Create: `internal/scaffold/template/application.yaml.tmpl`
- Create: `internal/scaffold/template/application-prod.yaml.example.tmpl`
- Create: `internal/scaffold/scaffold_test.go`
- Modify: `cmd/bear/main.go`
- Modify: `cmd/bear-cli/main.go`
- Modify: `cmd/bear-cli/cmd/gen.go`
- Modify: `cmd/bear-cli/cmd/new.go`
- Modify: `cmd/bear-cli/cmd/root.go`

**Interfaces:**
- Produces: `internal/cli.Execute(args []string, stdout, stderr io.Writer) int`.
- Produces: `scaffold.Generate(ctx, Options) error` with `Name`, `Module`, `Directory`, and `FrameworkVersion`.
- Preserves: both `go install .../cmd/bear` and `go install .../cmd/bear-cli` executable paths.

- [ ] **Step 1: Write a generated-project smoke test**

```go
func TestGenerateProjectBuildsTestsAndServesHealth(t *testing.T) {
	dir := t.TempDir()
	err := Generate(context.Background(), Options{
		Name: "billing-api", Module: "example.com/billing-api",
		Directory: dir, FrameworkVersion: "v0.10.0-rc.1",
	})
	if err != nil {
		t.Fatal(err)
	}
	runGo(t, dir, "mod", "edit", "-replace", "github.com/duiniwukenaihe/gin-bear="+repoRoot(t))
	runGo(t, dir, "mod", "tidy")
	runGo(t, dir, "test", "./...", "-count=1")
	runGeneratedServerHealthCheck(t, dir, "/live")
}
```

- [ ] **Step 2: Run it and confirm both existing generators fail the contract**

Run: `go test ./internal/scaffold -run TestGenerateProjectBuildsTestsAndServesHealth -count=1`

Expected: FAIL because the package and embedded template do not exist.

- [ ] **Step 3: Implement one command tree and embedded template**

```go
type Options struct {
	Name             string
	Module           string
	Directory        string
	FrameworkVersion string
}

//go:embed template/**
var templateFS embed.FS

func Generate(ctx context.Context, opts Options) error {
	if err := validateOptions(opts); err != nil {
		return err
	}
	return renderTemplateTree(templateFS, "template", opts.Directory, opts)
}
```

The generated `go.mod` must contain the requested module and
`require github.com/duiniwukenaihe/gin-bear v0.10.0-rc.1`. It must not contain
a copy of `pkg/bear`, `.git`, release docs, or repository CI files.

- [ ] **Step 4: Route both binaries through the same internal CLI**

```go
func main() {
	os.Exit(cli.Execute(os.Args[1:], os.Stdout, os.Stderr))
}
```

Keep old Cobra package entry points as thin deprecated delegates for source
compatibility. Replace every `os.Exit` below command handlers with returned
errors so tests can assert failures.

- [ ] **Step 5: Correct resource generation contracts**

Generate resource packages under `internal/<resource>`, name tests
`service_test.go`, use `decimal.Decimal` for decimal fields, and run `gofmt` on
every Go output before atomic rename. A generation error must leave no partial
package.

- [ ] **Step 6: Run CLI and generated-app tests**

Run: `go test ./internal/cli ./internal/scaffold ./cmd/bear-cli/cmd -count=1 && make verify`

Expected: both executable paths produce the same help and errors; a temporary
new project builds, tests, starts, answers `/live`, and exits on cancellation.

- [ ] **Step 7: Commit**

```bash
git add cmd/bear cmd/bear-cli internal/cli internal/scaffold go.mod go.sum
git commit -m "feat: 统一脚手架命令并验证生成项目可运行" -m "合并重复 CLI，改为内嵌版本化模板，保留旧安装入口，并增加生成、编译、测试、健康检查和退出的端到端验证。"
```

### Task 4: Introduce Application-Scoped Runtime And Deterministic Lifecycle

**Files:**
- Create: `pkg/bear/runtime.go`
- Create: `pkg/bear/lifecycle.go`
- Create: `pkg/bear/lifecycle_test.go`
- Modify: `pkg/bear/bear.go`
- Modify: `pkg/bear/ioc.go`
- Modify: `pkg/bear/log.go`
- Modify: `pkg/bear/health.go`

**Interfaces:**
- Produces: `Runtime`, `NewBeanFactory`, `ContextShutdowner`, and `Bear.Runtime()`.
- Preserves: package-level IoC and log helpers as default-runtime facades.

- [ ] **Step 1: Add tests for isolation, ordering, and failed listener cleanup**

```go
func TestApplicationsDoNotResolveEachOthersBeans(t *testing.T) {
	a := Ignite(NewSysConfig())
	b := Ignite(NewSysConfig())
	a.runtime.Container.Set(&namedBean{name: "a"})
	b.runtime.Container.Set(&namedBean{name: "b"})
	if got := Resolve[*namedBean](a.runtime.Container).name; got != "a" {
		t.Fatalf("app a resolved %q", got)
	}
}

func TestLifecycleInitializesFIFOAndShutsDownLIFO(t *testing.T) {
	var events []string
	l := newLifecycle()
	l.Add(recordingComponent{"first", &events})
	l.Add(recordingComponent{"second", &events})
	requireNoError(t, l.Start(context.Background()))
	requireNoError(t, l.Stop(context.Background()))
	assertStrings(t, events, []string{"start:first", "start:second", "stop:second", "stop:first"})
}
```

- [ ] **Step 2: Run the tests and verify global state/map order fail them**

Run: `go test ./pkg/bear -run 'TestApplicationsDoNotResolve|TestLifecycle' -count=20`

Expected: FAIL before the new runtime exists.

- [ ] **Step 3: Add scoped runtime while retaining the default facade**

```go
type Runtime struct {
	Config    *SysConfig
	Logger    *slog.Logger
	Container *BeanFactory
	Lifecycle *Lifecycle
	Metrics   *HTTPMetrics
}

type ContextShutdowner interface {
	ShutdownContext(context.Context) error
}

func NewBeanFactory() *BeanFactory {
	return &BeanFactory{beans: make(map[reflect.Type]any)}
}

func Resolve[T any](factory *BeanFactory) T {
	var zero T
	if factory == nil {
		return zero
	}
	value, _ := factory.Get(reflect.TypeOf((*T)(nil)).Elem()).(T)
	return value
}
```

`Ignite` creates one runtime and atomically publishes it as the legacy default.
All app-owned middleware receives the runtime directly.

- [ ] **Step 4: Coordinate listener startup and bounded shutdown**

Bind every enabled listener before starting any serve goroutine. Replace
`signal.Notify` with `signal.NotifyContext`. Use the configured shutdown timeout
for HTTP, hooks, and components. For any gRPC compatibility server, call
`GracefulStop` in a goroutine and fall back to `Stop` when the context expires.
Return `errors.Join` of serve and shutdown errors.

- [ ] **Step 5: Verify repeated and race execution**

Run: `go test ./pkg/bear -run 'TestApplicationsDoNotResolve|TestLifecycle|TestLaunch' -count=50 && go test -race ./pkg/bear -count=10`

Expected: deterministic PASS with no goroutine or listener leak.

- [ ] **Step 6: Commit**

```bash
git add pkg/bear/runtime.go pkg/bear/lifecycle.go pkg/bear/lifecycle_test.go pkg/bear/bear.go pkg/bear/ioc.go pkg/bear/log.go pkg/bear/health.go
git commit -m "refactor: 隔离应用运行时并统一生命周期" -m "将容器、日志、指标和组件顺序收归 Bear 实例，保留旧全局入口，并实现监听器预绑定、确定性初始化和有界倒序关闭。"
```

### Task 5: Rebuild Handler Compilation, Fairing Order, Binding, And Errors

**Files:**
- Create: `pkg/bear/handler.go`
- Create: `pkg/bear/binding.go`
- Create: `pkg/bear/http_error.go`
- Create: `pkg/bear/handler_test.go`
- Modify: `pkg/bear/responder.go`
- Modify: `pkg/bear/fairing.go`
- Modify: `pkg/bear/bear.go`
- Modify: `pkg/bear/error.go`

**Interfaces:**
- Produces: internal `compiledHandler func(*gin.Context) (any, error)`.
- Produces: public `WriteError(*gin.Context, error)` and `NewStatusError(status, code int, key string, cause error)`.
- Preserves: `Convert`, `Handle`, `HandleWithFairing`, `BearError`, and `NewError`.

- [ ] **Step 1: Write regressions for bound-method cache collision and response Fairings**

```go
func TestHandleKeepsBoundReceiverIdentity(t *testing.T) {
	a := &receiverHandler{value: "a"}
	b := &receiverHandler{value: "b"}
	app := Ignite(NewSysConfig())
	app.Handle(http.MethodGet, "/a", a.Get)
	app.Handle(http.MethodGet, "/b", b.Get)
	assertJSONValue(t, app, "/a", "a")
	assertJSONValue(t, app, "/b", "b")
}

func TestFairingPipelineWritesTransformedResultOnce(t *testing.T) {
	app := Ignite(NewSysConfig())
	app.Attach(&recordingFairing{name: "global"})
	app.HandleWithFairing(http.MethodGet, "/value", func() (string, error) {
		return "handler", nil
	}, &recordingFairing{name: "route"})
	assertBodyAndEvents(t, app, "/value", "route(global(handler))",
		[]string{"request:route", "request:global", "response:global", "response:route"})
}
```

- [ ] **Step 2: Add failures for status mapping, trailing JSON, and invalid signatures**

```go
func TestFairingUnauthorizedUsesHTTP401(t *testing.T) {
	app := Ignite(NewSysConfig())
	app.Attach(errorFairing{err: NewStatusError(401, 401, "error_unauthorized", nil)})
	app.Handle(http.MethodGet, "/private", func() string { return "secret" })
	assertStatus(t, app, "/private", http.StatusUnauthorized)
}

func TestBindingRejectsSecondJSONValue(t *testing.T) {
	request := newJSONRequest(`/users`, `{"name":"a"}{"name":"b"}`)
	assertRequestStatus(t, request, http.StatusBadRequest)
}
```

- [ ] **Step 3: Run focused tests and observe failures**

Run: `go test ./pkg/bear -run 'TestHandleKeepsBound|TestFairingPipeline|TestFairingUnauthorized|TestBindingRejectsSecond' -count=1`

Expected: FAIL due to pointer cache reuse, early response writes, fixed 400
responses, and acceptance of a second JSON value.

- [ ] **Step 4: Compile each actual handler value once and write once**

```go
type compiledHandler func(*gin.Context) (any, error)

func compileHandler(handler any) (compiledHandler, error) {
	value := reflect.ValueOf(handler)
	if !value.IsValid() || value.Kind() != reflect.Func {
		return nil, fmt.Errorf("handler must be a function, got %T", handler)
	}
	plan, err := compileArguments(value.Type())
	if err != nil {
		return nil, err
	}
	return func(ctx *gin.Context) (any, error) {
		args, err := plan.Bind(ctx)
		if err != nil {
			return nil, NewStatusError(400, 400, "error_invalid_params", err)
		}
		return decodeHandlerResults(value.Call(args))
	}, nil
}
```

Remove the global pointer cache and false warm-up. `Handle` panics during route
construction on an invalid signature; add `HandleE` for callers that prefer an
error. Standard `gin.HandlerFunc` remains supported but is documented as an
opaque response writer outside response Fairing transformation.

- [ ] **Step 5: Centralize safe errors and exact Fairing order**

Route request Fairings run before global request Fairings. After invocation,
global response Fairings run before route response Fairings, matching the
existing `HandleWithFairing` contract. Any Fairing error stops the pipeline and
uses `WriteError`. Unexpected error details stay in structured logs only.

- [ ] **Step 6: Run focused, full, and race tests**

Run: `go test ./pkg/bear -run 'TestHandle|TestFairing|TestBinding|TestError' -count=20 && make verify`

Expected: PASS; each request writes one response and preserves receiver identity.

- [ ] **Step 7: Commit**

```bash
git add pkg/bear/handler.go pkg/bear/binding.go pkg/bear/http_error.go pkg/bear/handler_test.go pkg/bear/responder.go pkg/bear/fairing.go pkg/bear/bear.go pkg/bear/error.go
git commit -m "fix: 重建处理器绑定和统一错误响应链路" -m "移除错误的函数指针缓存，启动时校验签名，修复 Fairing 后置处理，拒绝多值 JSON，并按 BearError 返回正确且不泄漏内部信息的状态码。"
```

### Task 6: Harden Configuration, HTTP Limits, Proxy Trust, And Authentication

**Files:**
- Create: `pkg/bear/config_loader.go`
- Create: `pkg/bear/config_loader_test.go`
- Create: `pkg/bear/middleware_security.go`
- Modify: `pkg/bear/config.go`
- Modify: `pkg/bear/middleware.go`
- Modify: `pkg/bear/jwt.go`
- Modify: `pkg/bear/jwt_fairing.go`
- Modify: `pkg/bear/auth_token.go`
- Modify: `application-prod.yaml.example`
- Modify: `docs/production.md`
- Modify: `docs/compatibility.md`

**Interfaces:**
- Produces: `LoadConfig(paths ...string) (*SysConfig, error)`.
- Preserves: `InitConfig()` as a panic-on-error compatibility wrapper.
- Adds: `server.max_request_body_bytes`, optional JWT issuer/audience, and explicit proxy trust.

- [ ] **Step 1: Write strict-config and HTTP-security tests**

```go
func TestLoadConfigRejectsUnknownProductionKey(t *testing.T) {
	path := writeConfig(t, "server:\n  port: 8080\n  name: app\n  typo_timeout: 3s\n")
	t.Setenv("BEAR_ENV", "prod")
	_, err := LoadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "typo_timeout") {
		t.Fatalf("expected unknown-field error, got %v", err)
	}
}

func TestDefaultProxyPolicyIgnoresSpoofedForwardedFor(t *testing.T) {
	app := Ignite(NewSysConfig())
	request := httptest.NewRequest(http.MethodGet, "/ip", nil)
	request.Header.Set("X-Forwarded-For", "203.0.113.9")
	assertClientIP(t, app, request, request.RemoteAddr)
}

func TestRequestBodyLimitReturns413(t *testing.T) {
	cfg := NewSysConfig()
	cfg.Server.MaxRequestBodyBytes = 32
	assertLargeJSONStatus(t, Ignite(cfg), http.StatusRequestEntityTooLarge)
}
```

- [ ] **Step 2: Run and verify failures**

Run: `go test ./pkg/bear -run 'TestLoadConfigRejectsUnknown|TestDefaultProxyPolicy|TestRequestBodyLimit' -count=1`

Expected: FAIL because parsing is permissive, Gin retains its default proxy
trust, and JSON bodies have no framework limit.

- [ ] **Step 3: Implement strict error-returning config loading**

Use `yaml.v3.Decoder.KnownFields(true)` in production and JSON
`DisallowUnknownFields`. Development may set `config.strict: false`; production
cannot. File syntax errors return immediately. `InitConfig` delegates to
`LoadConfig` and panics only to preserve its old signature.

- [ ] **Step 4: Apply secure HTTP defaults**

Set `MaxHeaderBytes` to 1 MiB when omitted, request body default to 1 MiB, and
call `SetTrustedProxies(nil)` unless explicit CIDRs are configured. Validate
client request IDs with `^[A-Za-z0-9._-]{1,128}$`. Add `nosniff`,
`X-Frame-Options: DENY`, and a conservative referrer policy; do not add HSTS.
CORS startup validation rejects wildcard origins with credentials.

- [ ] **Step 5: Make JWT and revocation behavior explicit**

JWT parsing requires HS256, validates optional issuer and audience, and uses a
configurable clock skew no greater than five minutes. If Redis is absent,
ordinary JWT validation succeeds while revoke returns a typed
`ErrTokenRevocationUnavailable`; it must never nil-dereference Redis.

- [ ] **Step 6: Correct the production example**

Remove `/metrics` from default public auth paths, set PostgreSQL
`sslmode: verify-full`, leave the password empty for `POSTGRES_PASSWORD`, set a
32+ character JWT placeholder that still fails the known-placeholder check,
and document every behavior change in `docs/compatibility.md`.

- [ ] **Step 7: Run all security and compatibility checks**

Run: `go test ./pkg/bear -run 'TestLoadConfig|TestProxy|TestRequestBody|TestJWT|TestAuthToken|TestCORS' -count=20 && make verify`

Expected: PASS with no secret, raw token, or unexpected error detail in output.

- [ ] **Step 8: Commit**

```bash
git add pkg/bear/config_loader.go pkg/bear/config_loader_test.go pkg/bear/config.go pkg/bear/middleware.go pkg/bear/middleware_security.go pkg/bear/jwt.go pkg/bear/jwt_fairing.go pkg/bear/auth_token.go application-prod.yaml.example docs/production.md docs/compatibility.md go.mod go.sum
git commit -m "feat: 强化生产配置和 HTTP 鉴权默认值" -m "增加严格配置加载、请求体与请求标识限制、显式代理信任、安全响应头、JWT 声明校验和无 Redis 降级语义，并修正生产配置示例。"
```

### Task 7: Fix Database, Redis, Rate Limiting, And Distributed Cron Correctness

**Files:**
- Create: `pkg/bear/context.go`
- Create: `pkg/bear/cron_lock.go`
- Create: `pkg/bear/cron_lock_test.go`
- Modify: `pkg/bear/db.go`
- Modify: `pkg/bear/redis.go`
- Modify: `pkg/bear/limiter.go`
- Modify: `pkg/bear/cron.go`
- Modify: `pkg/bear/config.go`
- Modify: `go.mod`
- Modify: `go.sum`

**Interfaces:**
- Produces: typed `WithUserID` and `UserIDFromContext` helpers.
- Produces: `OpenRedisAdapter(*RedisConfig) (*RedisAdapter, error)`.
- Preserves: `NewRedisAdapter` as a compatibility wrapper.
- Produces: owner-token compare-and-delete cron locks.

- [ ] **Step 1: Write data correctness regressions with miniredis**

```go
func TestCronLockCannotDeleteNewOwner(t *testing.T) {
	redisServer := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	first := newCronLock(client, "jobs:billing", "owner-a", time.Second)
	second := newCronLock(client, "jobs:billing", "owner-b", time.Second)
	requireNoError(t, first.Acquire(context.Background()))
	redisServer.FastForward(2 * time.Second)
	requireNoError(t, second.Acquire(context.Background()))
	requireNoError(t, first.Release(context.Background()))
	assertLockOwner(t, client, "jobs:billing", "owner-b")
}

func TestUserIDFromContextNeverPanicsOnNonStringValue(t *testing.T) {
	ctx := context.WithValue(context.Background(), legacyUserIDKey, uint(42))
	if got, ok := UserIDFromContext(ctx); !ok || got != "42" {
		t.Fatalf("got %q, %v", got, ok)
	}
}
```

- [ ] **Step 2: Run and verify the existing lock/context behavior fails**

Run: `go test ./pkg/bear -run 'TestCronLockCannotDeleteNewOwner|TestUserIDFromContextNeverPanics' -count=1`

Expected: FAIL due to unconditional `DEL` and unchecked string assertions.

- [ ] **Step 3: Implement owned locks and safe context values**

```go
const releaseOwnedLock = `
if redis.call("GET", KEYS[1]) == ARGV[1] then
  return redis.call("DEL", KEYS[1])
end
return 0`

func (l *cronLock) Release(ctx context.Context) error {
	return l.client.Eval(ctx, releaseOwnedLock, []string{l.key}, l.owner).Err()
}
```

Generate ownership with `crypto/rand`. Reject non-positive TTL. Log a warning
when execution reaches 80% of TTL and never release a lock that is no longer
owned.

- [ ] **Step 4: Separate database TLS semantics**

Add PostgreSQL `sslmode` validation and MySQL `tls` configuration. Keep
`DBConfig.SSLMode` as deprecated input: map it only for PostgreSQL and emit a
migration warning for MySQL. Build MySQL DSNs with `mysql.Config` rather than
string concatenation. Add tests for reserved characters in user/password/name.

- [ ] **Step 5: Return errors from Redis startup and define limiter policy**

`OpenRedisAdapter` returns ping errors. `NewRedisAdapter` preserves old behavior
but delegates to it. Add a named `LimiterFailureMode` (`open` or `closed`) while
keeping `FailClosed` operational. Validate positive limits/windows and include
standard `Retry-After` headers on 429 responses.

- [ ] **Step 6: Run database/Redis tests repeatedly and under race detection**

Run: `go test ./pkg/bear -run 'TestCron|TestUserID|TestBuildDSN|TestRedis|TestRateLimiter' -count=30 && go test -race ./pkg/bear -count=10`

Expected: PASS with no lock ownership loss or context panic.

- [ ] **Step 7: Commit**

```bash
git add pkg/bear/context.go pkg/bear/cron_lock.go pkg/bear/cron_lock_test.go pkg/bear/db.go pkg/bear/redis.go pkg/bear/limiter.go pkg/bear/cron.go pkg/bear/config.go go.mod go.sum
git commit -m "fix: 修复数据连接和分布式任务一致性" -m "使用驱动级 DSN 配置、安全上下文取值、显式 Redis 错误、标准限流退化策略和带所有权校验的定时任务锁，避免误删新锁和运行时 panic。"
```

### Task 8: Standardize Health, Metrics, Tracing, And OpenAPI Contracts

**Files:**
- Modify: `pkg/bear/metrics.go`
- Modify: `pkg/bear/health.go`
- Modify: `pkg/bear/tracing.go`
- Modify: `pkg/bear/openapi.go`
- Create: `pkg/bear/observability_test.go`
- Create: `pkg/bear/openapi_contract_test.go`
- Modify: `docs/production.md`
- Modify: `go.mod`
- Modify: `go.sum`

**Interfaces:**
- Produces: per-runtime Prometheus registry and preserves existing `gin_bear_*` metric names.
- Produces: sanitized readiness output and detailed internal logs.
- Preserves: `EnableHealth`, `EnableMetrics`, `EnableTracing`, and `GenerateOpenAPI`.

- [ ] **Step 1: Write isolation, redaction, and concurrency tests**

```go
func TestMetricsAreIsolatedPerApplication(t *testing.T) {
	a := Ignite(NewSysConfig()).EnableMetrics()
	b := Ignite(NewSysConfig()).EnableMetrics()
	performRequest(t, a, http.MethodGet, "/missing?token=secret")
	assertMetricCount(t, a, "gin_bear_http_requests_total", 1)
	assertMetricCount(t, b, "gin_bear_http_requests_total", 0)
}

func TestTracingDoesNotRecordRawQuery(t *testing.T) {
	span := traceRequest(t, "/users?access_token=secret")
	for _, attr := range span.Attributes() {
		if strings.Contains(attr.Value.AsString(), "secret") {
			t.Fatalf("trace leaked query value in %s", attr.Key)
		}
	}
}

func TestReadinessRunsChecksConcurrentlyAndSanitizesErrors(t *testing.T) {
	status, body, elapsed := runReadiness(t, twoSlowFailingChecks())
	if status != 503 || elapsed >= 150*time.Millisecond || strings.Contains(body, "password=") {
		t.Fatalf("status=%d elapsed=%s body=%s", status, elapsed, body)
	}
}
```

- [ ] **Step 2: Run and confirm global metrics, raw queries, and sequential checks fail**

Run: `go test ./pkg/bear -run 'TestMetricsAreIsolated|TestTracingDoesNotRecord|TestReadinessRunsChecks' -count=1`

Expected: FAIL on all three production properties.

- [ ] **Step 3: Move metrics to a standard per-app registry**

Use `github.com/prometheus/client_golang v1.23.2`, register Go/process
collectors, keep route-pattern labels only, and serve with `promhttp.HandlerFor`.
Metrics remain disabled unless explicitly enabled in newly generated production
configuration. Existing explicit calls continue to work.

- [ ] **Step 4: Sanitize telemetry and parallelize readiness**

Do not record `url.query`. Attach generated request ID from context, service
version, method, route, and status. Readiness checks run concurrently with one
child timeout each and deterministic sorted output. Public output contains only
`ok` or `failed`; structured logs keep wrapped causes.

- [ ] **Step 5: Validate generated OpenAPI as a contract**

Add tests for grouped paths, explicit public-route security, duplicate
operation IDs, request content type, 400/401/403/404/500 responses, and schema
references. Parse every generated document with a strict OpenAPI parser in
tests; generation returns an error for invalid or duplicate metadata.

- [ ] **Step 6: Run observability and full checks**

Run: `go test ./pkg/bear -run 'TestMetrics|TestTracing|TestReadiness|TestGenerateOpenAPI' -count=20 && make verify`

Expected: stable PASS, no unbounded metric labels, no raw query data, and valid
OpenAPI JSON.

- [ ] **Step 7: Commit**

```bash
git add pkg/bear/metrics.go pkg/bear/health.go pkg/bear/tracing.go pkg/bear/openapi.go pkg/bear/observability_test.go pkg/bear/openapi_contract_test.go docs/production.md go.mod go.sum
git commit -m "feat: 标准化健康检查和可观测性契约" -m "隔离 Prometheus 注册表，补充运行时指标，并发执行且脱敏就绪检查，移除追踪中的原始查询参数，并验证生成 OpenAPI 文档。"
```

### Task 9: Add Real Framework Examples, Upgrade Guidance, And CLI Release Governance

**Files:**
- Create: `examples/basic/main.go`
- Create: `examples/basic/main_test.go`
- Create: `examples/auth/main.go`
- Create: `examples/migration/main.go`
- Create: `CHANGELOG.md`
- Create: `docs/migration-v0.9-to-v0.10.md`
- Create: `.goreleaser.yml`
- Create: `.github/workflows/release.yml`
- Modify: `README.md`
- Modify: `SECURITY.md`
- Modify: `docs/runbook.md`
- Modify: `docs/production.md`

**Interfaces:**
- Produces: examples that compile in `go test ./...`.
- Produces: checksummed CLI binaries only; no container images.
- Documents: exact v0.9 to v0.10 behavior changes and rollback steps.

- [ ] **Step 1: Add documentation contract tests**

```go
func TestReleaseDocumentationNamesEveryCompatibilityChange(t *testing.T) {
	text := readFile(t, "../docs/migration-v0.9-to-v0.10.md")
	for _, phrase := range []string{
		"strict production configuration",
		"trusted proxies",
		"request body limit",
		"MySQL TLS",
		"metrics",
		"token revocation",
	} {
		if !strings.Contains(text, phrase) {
			t.Fatalf("migration guide missing %q", phrase)
		}
	}
}
```

- [ ] **Step 2: Write runnable examples and replace stale README snippets**

Every README code block that claims to compile must be sourced from one of the
example packages or tested as a generated fixture. Show `ApplyAll` error
handling, cancellable `Launch`, typed errors, production config loading, and
the canonical `cmd/bear` install path.

- [ ] **Step 3: Add a non-container CLI release workflow**

Trigger only on `v*` tags. Run `make verify`, then GoReleaser to build
`cmd/bear` for Linux, macOS, and Windows on amd64/arm64 where supported. Publish
archives, SHA-256 checksums, changelog text, and source provenance metadata.
Set workflow permissions to `contents: write` only in the release job.

- [ ] **Step 4: Verify release configuration without publishing**

Run: `goreleaser release --snapshot --clean`

Expected: CLI archives and checksums are produced under `dist/`; no image,
Dockerfile, manifest, or container registry action is produced.

- [ ] **Step 5: Commit**

```bash
git add examples CHANGELOG.md docs README.md SECURITY.md .goreleaser.yml .github/workflows/release.yml
git commit -m "docs: 完成升级指南和框架发布治理" -m "增加可编译示例、v0.9 到 v0.10 迁移说明、变更日志、安全支持范围和仅发布 CLI 二进制及校验和的版本流程。"
```

### Task 10: Raise Coverage, Execute The Release Candidate Gate, And Audit Compatibility

**Files:**
- Modify: `scripts/check-coverage.sh`
- Modify: `scripts/release-check.sh`
- Modify: `docs/runbook.md`
- Modify: `CHANGELOG.md`
- Add tests beside every core file still below the risk-based target.

**Interfaces:**
- Raises: overall statement coverage floor to 70%.
- Requires: 80%+ coverage for handler, binding, errors, config loader, lifecycle, auth, migration lock, cron lock, and CLI scaffold packages.
- Produces: a documented `v0.10.0-rc.1` release-candidate result.

- [ ] **Step 1: Generate a function-level coverage gap report**

Run: `go test ./... -coverprofile=coverage.out -count=1 && go tool cover -func=coverage.out | sort -k3n`

Expected: a list of remaining uncovered functions. Add behavior tests for
critical branches; do not add assertion-free tests or exclude files.

- [ ] **Step 2: Raise and verify the final coverage floor**

Change `COVERAGE_MINIMUM` default in `scripts/check-coverage.sh` from `60.0` to
`70.0` after tests pass.

Run: `COVERAGE_MINIMUM=70.0 scripts/release-check.sh`

Expected: PASS.

- [ ] **Step 3: Run clean, repeated, and race verification**

Run:

```bash
go clean -testcache
go test ./... -count=1
go test ./... -shuffle=on -count=20
go test -race ./... -count=3
go vet ./...
go run honnef.co/go/tools/cmd/staticcheck@v0.7.0 ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...
scripts/release-check.sh
git diff --check
```

Expected: every command passes and `govulncheck` reports no reachable known
vulnerability.

- [ ] **Step 4: Exercise old and new applications**

Build and run one fixture using the v0.9-style API and one newly generated app.
For both, verify startup, `/live`, `/ready`, one success route, one validation
error, one unauthorized error, SIGTERM shutdown, and absence of leaked secrets
in logs/traces.

- [ ] **Step 5: Review scope and repository hygiene**

Run:

```bash
git status --short
git branch --format='%(refname:short)'
git ls-remote --heads origin
find . -maxdepth 3 -type f \( -name 'Dockerfile' -o -name 'docker-compose.yml' -o -path '*/kubernetes/*' \)
```

Expected: only `main` and `codex/production-baseline` exist locally/remotely;
no container/Kubernetes files exist; the worktree contains only planned files.

- [ ] **Step 6: Commit the release-candidate gate**

```bash
git add scripts docs/runbook.md CHANGELOG.md pkg cmd internal examples
git commit -m "test: 完成生产框架发布候选验证" -m "将总覆盖率提升到 70%，关键链路达到 80%，完成重复、乱序、竞态、静态分析、漏洞、旧项目兼容和新项目端到端验证，并记录 v0.10.0-rc.1 结果。"
```

- [ ] **Step 7: Create the release candidate only after human review**

Run: `git tag -s v0.10.0-rc.1 -m "gin-bear v0.10.0-rc.1"`

Expected: a signed local tag. Push the tag only after the user reviews the
verification report and migration guide.

## Final Acceptance Criteria

- `main` is green under build, unit, shuffle, race, vet, staticcheck, pinned
  vulnerability scanning, module tidiness, generated-project smoke tests, and
  coverage gates.
- Existing v0.9-style applications compile without public API removals.
- New projects are generated from one CLI and do not copy framework source.
- Runtime state is isolated per application and lifecycle order is deterministic.
- Handler receivers, Fairing responses, HTTP statuses, and error redaction are correct.
- Request bodies, request IDs, CORS, proxy headers, JWT claims, and production
  configuration have bounded or explicit behavior.
- Database TLS, Redis failure behavior, rate limits, and cron locks are tested.
- Metrics, readiness, traces, and OpenAPI are isolated, bounded, sanitized, and validated.
- No Docker or Kubernetes asset is reintroduced.
- The migration guide names every intentional behavior change before any tag is pushed.

# Production Baseline Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Harden gin-bear's default runtime behavior for a production baseline.

**Architecture:** Keep the public API stable and make focused changes inside `pkg/bear`. Add test coverage around startup config, HTTP server construction, CORS behavior, optional database validation, and safe error responses.

**Tech Stack:** Go, Gin, `net/http`, slog, GORM config structs, Go testing package, `httptest`.

---

## File Structure

- Modify: `pkg/bear/config.go` for server timeout and CORS config.
- Modify: `pkg/bear/bear.go` for startup ordering, optional database validation, Gin runtime setup, and HTTP server construction helper.
- Modify: `pkg/bear/middleware.go` for config-driven CORS and stable validation error responses.
- Modify: `pkg/bear/responder.go` for safe client-facing handler errors.
- Create: `pkg/bear/production_baseline_test.go` for P0 behavior tests.

---

### Task 1: Optional Database Startup

**Files:**
- Test: `pkg/bear/production_baseline_test.go`
- Modify: `pkg/bear/bear.go`

- [ ] **Step 1: Write the failing test**

```go
func TestIgniteAllowsDatabaseDisabledWithoutDSN(t *testing.T) {
	resetTestInjector()
	cfg := NewSysConfig()
	cfg.DB.Enabled = false
	cfg.DB.DSN = ""
	cfg.DB.DBName = ""

	app := Ignite(cfg)

	if app == nil {
		t.Fatal("expected Ignite to return an app")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `GOPROXY=https://goproxy.cn,direct go test ./pkg/bear -run TestIgniteAllowsDatabaseDisabledWithoutDSN -count=1`

Expected before implementation: panic with `database configuration is required`.

- [ ] **Step 3: Write minimal implementation**

In `Ignite`, replace unconditional DB validation with:

```go
if config.DB != nil && config.DB.Enabled && config.DB.DSN == "" && config.DB.DBName == "" {
	panic("database configuration is required when database.enabled=true (dsn or dbname)")
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `GOPROXY=https://goproxy.cn,direct go test ./pkg/bear -run TestIgniteAllowsDatabaseDisabledWithoutDSN -count=1`

Expected: PASS.

---

### Task 2: Startup Ordering and Middleware Config

**Files:**
- Test: `pkg/bear/production_baseline_test.go`
- Modify: `pkg/bear/bear.go`

- [ ] **Step 1: Write the failing test**

```go
func TestIgniteRegistersProvidedConfigBeforeMiddleware(t *testing.T) {
	resetTestInjector()
	cfg := NewSysConfig()
	cfg.DB.Enabled = false
	cfg.Middleware.PerformanceLogLevel = "debug"
	cfg.Middleware.SlowRequestThreshold = "250ms"

	app := Ignite(cfg)

	if got := GetByType[*SysConfig](); got != cfg {
		t.Fatal("expected provided config to be registered")
	}
	if len(app.Handlers) == 0 {
		t.Fatal("expected base middleware to be registered")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `GOPROXY=https://goproxy.cn,direct go test ./pkg/bear -run TestIgniteRegistersProvidedConfigBeforeMiddleware -count=1`

Expected before implementation: config is registered after middleware construction.

- [ ] **Step 3: Write minimal implementation**

Reorder `Ignite` so config loading, `SetDefaultLogger`, and IoC registration happen before `RequestIDMiddleware`, `PerformanceMiddleware`, and `RecoveryMiddleware` are added.

- [ ] **Step 4: Run test to verify it passes**

Run: `GOPROXY=https://goproxy.cn,direct go test ./pkg/bear -run TestIgniteRegistersProvidedConfigBeforeMiddleware -count=1`

Expected: PASS.

---

### Task 3: HTTP Server Timeout Defaults

**Files:**
- Test: `pkg/bear/production_baseline_test.go`
- Modify: `pkg/bear/config.go`
- Modify: `pkg/bear/bear.go`

- [ ] **Step 1: Write the failing test**

```go
func TestBuildHTTPServerAppliesConfiguredTimeouts(t *testing.T) {
	resetTestInjector()
	cfg := NewSysConfig()
	cfg.Server.Port = 9099
	cfg.Server.ReadHeaderTimeout = "2s"
	cfg.Server.ReadTimeout = "3s"
	cfg.Server.WriteTimeout = "4s"
	cfg.Server.IdleTimeout = "5s"
	cfg.Server.MaxHeaderBytes = 8192

	app := Ignite(cfg)
	srv := app.buildHTTPServer(cfg)

	if srv.Addr != ":9099" {
		t.Fatalf("addr = %q", srv.Addr)
	}
	if srv.ReadHeaderTimeout != 2*time.Second {
		t.Fatalf("ReadHeaderTimeout = %s", srv.ReadHeaderTimeout)
	}
	if srv.ReadTimeout != 3*time.Second {
		t.Fatalf("ReadTimeout = %s", srv.ReadTimeout)
	}
	if srv.WriteTimeout != 4*time.Second {
		t.Fatalf("WriteTimeout = %s", srv.WriteTimeout)
	}
	if srv.IdleTimeout != 5*time.Second {
		t.Fatalf("IdleTimeout = %s", srv.IdleTimeout)
	}
	if srv.MaxHeaderBytes != 8192 {
		t.Fatalf("MaxHeaderBytes = %d", srv.MaxHeaderBytes)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `GOPROXY=https://goproxy.cn,direct go test ./pkg/bear -run TestBuildHTTPServerAppliesConfiguredTimeouts -count=1`

Expected before implementation: missing fields or helper.

- [ ] **Step 3: Write minimal implementation**

Add timeout string fields to `ServerConfig`, defaults in `NewSysConfig`, a duration parser helper, and `(*Bear).buildHTTPServer`.

- [ ] **Step 4: Run test to verify it passes**

Run: `GOPROXY=https://goproxy.cn,direct go test ./pkg/bear -run TestBuildHTTPServerAppliesConfiguredTimeouts -count=1`

Expected: PASS.

---

### Task 4: Config-Driven CORS

**Files:**
- Test: `pkg/bear/production_baseline_test.go`
- Modify: `pkg/bear/config.go`
- Modify: `pkg/bear/middleware.go`

- [ ] **Step 1: Write the failing test**

```go
func TestCORSMiddlewareUsesConfiguredOrigin(t *testing.T) {
	resetTestInjector()
	cfg := NewSysConfig()
	cfg.CORS.Enabled = true
	cfg.CORS.AllowOrigins = []string{"https://example.com"}
	cfg.CORS.AllowCredentials = true
	GetInjector().Set(cfg)

	router := gin.New()
	router.Use(CORSMiddleware())
	router.GET("/ping", func(c *gin.Context) { c.String(http.StatusOK, "pong") })

	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	req.Header.Set("Origin", "https://example.com")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://example.com" {
		t.Fatalf("origin header = %q", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Fatalf("credentials header = %q", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `GOPROXY=https://goproxy.cn,direct go test ./pkg/bear -run TestCORSMiddlewareUsesConfiguredOrigin -count=1`

Expected before implementation: hard-coded wildcard origin.

- [ ] **Step 3: Write minimal implementation**

Add `CORSConfig` to `SysConfig` and have `CORSMiddleware` emit headers only when enabled and origin is allowed.

- [ ] **Step 4: Run test to verify it passes**

Run: `GOPROXY=https://goproxy.cn,direct go test ./pkg/bear -run TestCORSMiddlewareUsesConfiguredOrigin -count=1`

Expected: PASS.

---

### Task 5: Safe Error Responses

**Files:**
- Test: `pkg/bear/production_baseline_test.go`
- Modify: `pkg/bear/responder.go`
- Modify: `pkg/bear/middleware.go`

- [ ] **Step 1: Write the failing tests**

```go
func TestHandleErrorHidesUnexpectedErrorDetails(t *testing.T) {
	resetTestInjector()
	cfg := NewSysConfig()
	cfg.DB.Enabled = false
	app := Ignite(cfg)
	app.GET("/boom", Convert(func() (string, error) {
		return "", errors.New("sql: password=secret")
	}))

	req := httptest.NewRequest(http.MethodGet, "/boom", nil)
	w := httptest.NewRecorder()
	app.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", w.Code)
	}
	if strings.Contains(w.Body.String(), "password=secret") {
		t.Fatalf("response leaked internal error: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Internal server error") {
		t.Fatalf("response missing safe error message: %s", w.Body.String())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `GOPROXY=https://goproxy.cn,direct go test ./pkg/bear -run TestHandleErrorHidesUnexpectedErrorDetails -count=1`

Expected before implementation: response contains raw error.

- [ ] **Step 3: Write minimal implementation**

Update `handleError` to return `Internal server error` for non-`BearError` errors and include request id if present.

- [ ] **Step 4: Run test to verify it passes**

Run: `GOPROXY=https://goproxy.cn,direct go test ./pkg/bear -run TestHandleErrorHidesUnexpectedErrorDetails -count=1`

Expected: PASS.

---

### Task 6: Full Verification

**Files:**
- No new source files.

- [ ] **Step 1: Run focused package tests**

Run: `GOPROXY=https://goproxy.cn,direct go test ./pkg/bear -count=1`

Expected: PASS.

- [ ] **Step 2: Run all tests**

Run: `GOPROXY=https://goproxy.cn,direct go test ./... -count=1`

Expected: PASS or dependency download/network failure explicitly recorded.

- [ ] **Step 3: Run vet**

Run: `GOPROXY=https://goproxy.cn,direct go vet ./...`

Expected: PASS or dependency download/network failure explicitly recorded.

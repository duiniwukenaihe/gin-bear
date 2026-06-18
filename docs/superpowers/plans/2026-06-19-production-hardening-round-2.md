# Production Hardening Round 2 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Harden high-risk runtime defaults for production use.

**Architecture:** Keep the public framework API mostly unchanged and add small focused helpers around binding, route group initialization, limiter degradation, WebSocket origin policy, repository updates, and plugin policy. Existing compatibility remains the default where safe, while production-unsafe behavior becomes explicit.

**Tech Stack:** Go, Gin, GORM, go-redis, gorilla/websocket.

---

### Task 1: Tests First

**Files:**
- Modify: `pkg/bear/production_baseline_test.go`

- [x] Add failing tests for mixed binding, direct route registration, Redis fail-closed, WebSocket allowlist/production validation, non-versioned repository update, and plugin policy.
- [x] Run: `GOPROXY=https://goproxy.cn,direct go test ./pkg/bear -count=1`
- [x] Expected: new tests fail because behavior is not implemented yet.

### Task 2: Runtime Hardening

**Files:**
- Modify: `pkg/bear/responder.go`
- Modify: `pkg/bear/bear.go`
- Modify: `pkg/bear/config.go`
- Modify: `pkg/bear/limiter.go`
- Modify: `pkg/bear/db.go`
- Modify: `pkg/bear/plugin.go`

- [x] Implement multi-source request binding.
- [x] Initialize the root router group lazily for direct route registration.
- [x] Add rate limiter fail-closed option.
- [x] Add WebSocket allowed origin config and production validation.
- [x] Change non-versioned repository update away from full-row `Save`.
- [x] Add plugin enablement and directory allowlist checks.
- [x] Run: `GOPROXY=https://goproxy.cn,direct go test ./pkg/bear -count=1`
- [x] Expected: package tests pass.

### Task 3: Full Verification And Commit

**Files:**
- All changed files

- [x] Run: `gofmt` on changed Go files.
- [x] Run: `GOPROXY=https://goproxy.cn,direct go test ./... -count=1`
- [x] Run: `GOPROXY=https://goproxy.cn,direct go vet ./...`
- [x] Commit with a detailed Chinese commit message.

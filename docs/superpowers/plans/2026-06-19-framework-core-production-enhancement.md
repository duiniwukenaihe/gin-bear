# Framework Core Production Enhancement Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Improve gin-bear's core framework scaffold quality and production runtime configurability without adding deployment packaging.

**Architecture:** Keep changes local to existing framework and CLI surfaces. Generator behavior stays in `cmd/bear-cli/cmd/gen.go`; runtime configuration stays in `pkg/bear/config.go`, `pkg/bear/bear.go`, `pkg/bear/health.go`, `pkg/bear/log.go`, and `pkg/bear/redis.go`. Documentation updates explain framework production usage only.

**Tech Stack:** Go, Gin, slog, go-redis, GORM, Cobra, repository Go tests.

---

### Task 1: Generated API Correctness

**Files:**
- Modify: `cmd/bear-cli/cmd/gen.go`
- Modify: `cmd/bear-cli/cmd/gen_test.go`

- [x] Add failing tests for field parsing, dashed resource names, DTO-to-model mapping, pointer update DTOs, update guards, and pagination normalization.
- [x] Implement field metadata helpers for safe package names, exported names, route names, time imports, create assignments, update pointer fields, update guards, and response mapping.
- [x] Verify generated API packages compile with representative fields.

### Task 2: Runtime Configuration

**Files:**
- Modify: `pkg/bear/config.go`
- Modify: `pkg/bear/bear.go`
- Modify: `pkg/bear/health.go`
- Modify: `pkg/bear/log.go`
- Modify: `pkg/bear/redis.go`
- Modify: `pkg/bear/production_baseline_test.go`

- [x] Add failing tests for `server.shutdown_timeout`, `health.readiness_timeout`, `log.level`, expanded environment overrides, and `redis.required`.
- [x] Add config structs, defaults, semantic validation, and environment overrides.
- [x] Apply configured shutdown timeout, readiness timeout, logger level, and Redis required startup behavior.

### Task 3: Docs And Verification

**Files:**
- Modify: `application-prod.yaml.example`
- Modify: `docs/production.md`
- Modify: `docs/runbook.md`
- Modify: `README.md`
- Modify: `scripts/release_check_test.go`

- [x] Update examples and docs for the new framework-level production knobs.
- [x] Ensure release checks remain focused on Go framework validation.
- [x] Run full verification: `go test`, race tests, vet, govulncheck, and `scripts/release-check.sh`.

Verification completed:

```bash
GOPROXY=https://goproxy.cn,direct go test ./... -count=1
GOPROXY=https://goproxy.cn,direct go test -race ./... -count=1
GOPROXY=https://goproxy.cn,direct go vet ./...
GOPROXY=https://goproxy.cn,direct $(go env GOPATH)/bin/govulncheck ./...
GOPROXY=https://goproxy.cn,direct scripts/release-check.sh
git diff --check
```

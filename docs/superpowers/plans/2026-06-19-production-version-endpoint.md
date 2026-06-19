# Production Version Endpoint Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `/version` endpoint backed by ldflags-injected build metadata.

**Architecture:** Add `BuildInfo` helpers in `pkg/bear`, expose them from `HealthController`, and update Docker/build docs to inject values with `-ldflags -X`. Keep the endpoint dependency-free and enabled through `EnableHealth()`.

**Tech Stack:** Go runtime build metadata, Gin health endpoints, Docker build args.

---

### Task 1: Version Endpoint Tests

**Files:**
- Modify: `pkg/bear/production_baseline_test.go`

- [x] Write a failing test that sets build variables, enables health endpoints, requests `/version`, and checks JSON fields.
- [x] Run: `GOPROXY=https://goproxy.cn,direct go test ./pkg/bear -count=1`
- [x] Expected before implementation: test fails because `/version` is not registered.

### Task 2: Build Info Implementation

**Files:**
- Create: `pkg/bear/version.go`
- Modify: `pkg/bear/health.go`
- Modify: `pkg/bear/config.go`

- [x] Define `Version`, `Commit`, `BuildTime` and `BuildInfo`.
- [x] Implement `GetBuildInfo()`.
- [x] Register `/version` from `HealthController`.
- [x] Add `/version` to default public auth paths.
- [x] Run: `GOPROXY=https://goproxy.cn,direct go test ./pkg/bear -count=1`
- [x] Expected after implementation: package tests pass.

### Task 3: Build Injection Docs

**Files:**
- Modify: `Dockerfile`
- Modify: `cmd/bear/main.go`
- Modify: `docs/production.md`

- [x] Add Docker build args and ldflags injection.
- [x] Update generated-project Dockerfile template.
- [x] Document local build command with version metadata.
- [x] Run full verification commands.
- [x] Commit with a detailed Chinese commit message.

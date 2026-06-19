# Production Metrics Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add built-in Prometheus text metrics for HTTP requests.

**Architecture:** Reuse `PerformanceMiddleware` as the recording point, add a small concurrency-safe in-memory metrics registry in `pkg/bear`, and expose it through `EnableMetrics`. Keep the implementation dependency-free and compatible with existing `MetricsConfig`.

**Tech Stack:** Go, Gin, Prometheus text exposition format.

---

### Task 1: Metrics Tests

**Files:**
- Modify: `pkg/bear/production_baseline_test.go`

- [x] Write a failing test that calls `EnableMetrics`, sends successful and failing requests, scrapes `/metrics`, and checks request counters, error counters, and histogram lines.
- [x] Run: `GOPROXY=https://goproxy.cn,direct go test ./pkg/bear -count=1`
- [x] Expected before implementation: test fails because `EnableMetrics` does not register a metrics endpoint.

### Task 2: Metrics Implementation

**Files:**
- Create: `pkg/bear/metrics.go`
- Modify: `pkg/bear/middleware.go`
- Modify: `pkg/bear/bear.go`

- [x] Implement a concurrency-safe metrics registry.
- [x] Record method, route, status, and duration from `PerformanceMiddleware`.
- [x] Implement Prometheus text rendering.
- [x] Register `EnableMetrics` using configured path, defaulting to `/metrics`.
- [x] Let `EnableHealth` enable metrics when configured.
- [x] Run: `GOPROXY=https://goproxy.cn,direct go test ./pkg/bear -count=1`
- [x] Expected after implementation: package tests pass.

### Task 3: Docs And Verification

**Files:**
- Modify: `docs/production.md`
- Modify: `application-prod.yaml.example`
- Modify: `cmd/bear/main.go`

- [x] Document the `/metrics` endpoint and metric names.
- [x] Add `metrics` config to production examples and old CLI generated config.
- [x] Run: `gofmt` on changed Go files.
- [x] Run: `GOPROXY=https://goproxy.cn,direct go test ./... -count=1`
- [x] Run: `GOPROXY=https://goproxy.cn,direct go test -race ./... -count=1`
- [x] Run: `GOPROXY=https://goproxy.cn,direct go vet ./...`
- [x] Run: `GOPROXY=https://goproxy.cn,direct govulncheck ./...`
- [x] Commit with a detailed Chinese commit message.

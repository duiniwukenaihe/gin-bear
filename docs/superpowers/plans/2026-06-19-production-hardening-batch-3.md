# Production Hardening Batch 3 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the next major production gaps in tracing, migrations, OpenAPI security documentation, and release checks in one cohesive batch.

**Architecture:** Keep changes local to existing framework surfaces: `pkg/bear/tracing.go` for OpenTelemetry, `pkg/bear/migration.go` for migration execution, `pkg/bear/openapi.go` for contract output, and `scripts/release-check.sh` plus CI for release checks. Use focused tests in `pkg/bear/production_baseline_test.go` and a small CI/script test file.

**Tech Stack:** Go, Gin, OpenTelemetry, SQL migrations, OpenAPI 3.0, Bash, GitHub Actions.

---

### Task 1: HTTP Tracing

**Files:**
- Create: `pkg/bear/tracing.go`
- Modify: `pkg/bear/bear.go`
- Test: `pkg/bear/production_baseline_test.go`

- [x] **Step 1: Write failing tests for span creation and traceparent propagation.**
- [x] **Step 2: Run `GOPROXY=https://goproxy.cn,direct go test ./pkg/bear -run 'TestTracing' -count=1` and confirm failure.**
- [x] **Step 3: Implement OpenTelemetry HTTP middleware and `EnableTracing`.**
- [x] **Step 4: Re-run the tracing tests and confirm they pass.**

### Task 2: Migration Lock And Rollback

**Files:**
- Modify: `pkg/bear/migration.go`
- Test: `pkg/bear/production_baseline_test.go`

- [x] **Step 1: Write failing tests for `Down` rollback and stale-free lock cleanup.**
- [x] **Step 2: Run `GOPROXY=https://goproxy.cn,direct go test ./pkg/bear -run 'TestMigrationRunner' -count=1` and confirm failure.**
- [x] **Step 3: Implement portable migration lock, `Down`, latest-applied lookup, and transactional down execution.**
- [x] **Step 4: Re-run migration tests and confirm they pass.**

### Task 3: OpenAPI JWT Security

**Files:**
- Modify: `pkg/bear/openapi.go`
- Test: `pkg/bear/production_baseline_test.go`
- Modify: `docs/production.md`

- [x] **Step 1: Write a failing test that expects `components.securitySchemes.BearerAuth` and top-level `security`.**
- [x] **Step 2: Run `GOPROXY=https://goproxy.cn,direct go test ./pkg/bear -run TestGenerateOpenAPIIncludesJWTSecurityScheme -count=1` and confirm failure.**
- [x] **Step 3: Generate the JWT bearer security scheme when auth config exists.**
- [x] **Step 4: Re-run OpenAPI tests and confirm they pass.**

### Task 4: Release Check Script And CI Alignment

**Files:**
- Create: `scripts/release-check.sh`
- Modify: `.github/workflows/ci.yml`
- Create: `scripts/release_check_test.go`
- Modify: `docs/production.md`

- [x] **Step 1: Write failing tests that require the release script to exist, be executable, include release gates, and be called by CI.**
- [x] **Step 2: Run `GOPROXY=https://goproxy.cn,direct go test ./scripts -count=1` and confirm failure.**
- [x] **Step 3: Add the script and update CI to call it before Docker build.**
- [x] **Step 4: Re-run script tests and confirm they pass.**

### Task 5: Full Verification And Commit

**Files:**
- Modify: `docs/superpowers/plans/2026-06-19-production-hardening-batch-3.md`

- [x] **Step 1: Run `GOPROXY=https://goproxy.cn,direct go build ./cmd ./cmd/bear ./cmd/bear-cli`.**
- [x] **Step 2: Run `GOPROXY=https://goproxy.cn,direct go test ./... -count=1`.**
- [x] **Step 3: Run `GOPROXY=https://goproxy.cn,direct go test -race ./... -count=1`.**
- [x] **Step 4: Run `GOPROXY=https://goproxy.cn,direct go vet ./...`.**
- [x] **Step 5: Run `GOPROXY=https://goproxy.cn,direct $(go env GOPATH)/bin/govulncheck ./...`.**
- [x] **Step 6: Run `GOPROXY=https://goproxy.cn,direct go mod tidy && git diff --exit-code -- go.mod go.sum`.**
- [x] **Step 7: Try `docker build .` and record whether the local Docker daemon is available.**
- [ ] **Step 8: Commit with a complete Chinese message and push `codex/production-baseline`.**

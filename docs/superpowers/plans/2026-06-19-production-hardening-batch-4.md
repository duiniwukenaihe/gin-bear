# Production Hardening Batch 4 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Improve production readiness with semantic config validation, accurate OpenAPI auth exceptions, migration lock recovery, Docker context hygiene, and CI SBOM artifacts.

**Architecture:** Extend existing framework surfaces instead of adding a new subsystem: config validation stays in `pkg/bear/config.go`, OpenAPI auth logic reuses public path matching, migration recovery stays on `MigrationRunner`, and supply-chain hygiene is handled by `.dockerignore`, `scripts/release-check.sh`, and CI.

**Tech Stack:** Go, Gin, OpenAPI 3.0, SQL migrations, Bash, Docker, GitHub Actions.

---

### Task 1: Semantic Config Validation

**Files:**
- Modify: `pkg/bear/config.go`
- Test: `pkg/bear/production_baseline_test.go`

- [x] **Step 1: Add failing tests for invalid tracing exporter, sample rate, metrics path, and timeout duration.**
- [x] **Step 2: Run `GOPROXY=https://goproxy.cn,direct go test ./pkg/bear -run TestSysConfigValidateRejectsSemanticErrors -count=1` and confirm failure.**
- [x] **Step 3: Implement semantic validation after struct validation.**
- [x] **Step 4: Re-run the config validation test and confirm it passes.**

### Task 2: OpenAPI Public Security Override

**Files:**
- Modify: `pkg/bear/openapi.go`
- Test: `pkg/bear/production_baseline_test.go`

- [x] **Step 1: Add a failing test for a public route operation with `security: []`.**
- [x] **Step 2: Run `GOPROXY=https://goproxy.cn,direct go test ./pkg/bear -run TestGenerateOpenAPIMarksPublicPathsWithoutSecurity -count=1` and confirm failure.**
- [x] **Step 3: Add operation-level security override for routes matching auth public paths.**
- [x] **Step 4: Re-run the OpenAPI public path test and confirm it passes.**

### Task 3: Migration Lock Recovery

**Files:**
- Modify: `pkg/bear/migration.go`
- Test: `pkg/bear/production_baseline_test.go`

- [x] **Step 1: Add a failing test for `ForceUnlock`.**
- [x] **Step 2: Run `GOPROXY=https://goproxy.cn,direct go test ./pkg/bear -run TestMigrationRunnerForceUnlockReleasesHeldLock -count=1` and confirm failure.**
- [x] **Step 3: Implement `ForceUnlock(ctx) error`.**
- [x] **Step 4: Re-run the migration unlock test and confirm it passes.**

### Task 4: Docker Context And SBOM CI

**Files:**
- Create: `.dockerignore`
- Modify: `cmd/bear/main.go`
- Modify: `cmd/bear/main_test.go`
- Modify: `scripts/release-check.sh`
- Modify: `scripts/release_check_test.go`
- Modify: `.github/workflows/ci.yml`
- Modify: `docs/production.md`

- [x] **Step 1: Add failing tests for `.dockerignore`, generated `.dockerignore`, forced SBOM generation, and CI artifact upload.**
- [x] **Step 2: Run `GOPROXY=https://goproxy.cn,direct go test ./cmd/bear ./scripts -count=1` and confirm failure.**
- [x] **Step 3: Add `.dockerignore`, generator template, release script SBOM mode, and CI upload artifact step.**
- [x] **Step 4: Re-run the command/script tests and confirm they pass.**

### Task 5: Full Verification And Commit

- [x] **Step 1: Run `GOPROXY=https://goproxy.cn,direct go build ./cmd ./cmd/bear ./cmd/bear-cli`.**
- [x] **Step 2: Run `GOPROXY=https://goproxy.cn,direct go test ./... -count=1`.**
- [x] **Step 3: Run `GOPROXY=https://goproxy.cn,direct go test -race ./... -count=1`.**
- [x] **Step 4: Run `GOPROXY=https://goproxy.cn,direct go vet ./...`.**
- [x] **Step 5: Run `GOPROXY=https://goproxy.cn,direct $(go env GOPATH)/bin/govulncheck ./...`.**
- [x] **Step 6: Run `GOPROXY=https://goproxy.cn,direct scripts/release-check.sh`.**
- [x] **Step 7: Try `docker build .` and record Docker daemon status.**
- [ ] **Step 8: Commit with a complete Chinese message and push `codex/production-baseline`.**

# Production Hardening Batch 5 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Improve production API contracts, migration safety, container metadata, dependency maintenance, and operational docs.

**Architecture:** Extend existing OpenAPI generation, migration runner validation, Dockerfile templates, and repository metadata without adding new runtime subsystems.

**Tech Stack:** Go, OpenAPI 3.0, SQL migrations, Docker, GitHub Actions, Dependabot.

---

### Task 1: OpenAPI Error Responses

**Files:**
- Modify: `pkg/bear/openapi.go`
- Test: `pkg/bear/production_baseline_test.go`

- [x] **Step 1: Add failing tests for `ErrorResponse`, `400`, `500`, and conditional `401`.**
- [x] **Step 2: Run `GOPROXY=https://goproxy.cn,direct go test ./pkg/bear -run TestGenerateOpenAPIIncludesStandardErrorResponses -count=1` and confirm failure.**
- [x] **Step 3: Add reusable error schema and response references.**
- [x] **Step 4: Re-run the OpenAPI test and confirm it passes.**

### Task 2: Migration Identifier Safety

**Files:**
- Modify: `pkg/bear/migration.go`
- Test: `pkg/bear/production_baseline_test.go`

- [x] **Step 1: Add failing tests for invalid `Table` and `LockTable`.**
- [x] **Step 2: Run `GOPROXY=https://goproxy.cn,direct go test ./pkg/bear -run TestMigrationRunnerRejectsUnsafeTableNames -count=1` and confirm failure.**
- [x] **Step 3: Validate SQL identifiers before formatting table names.**
- [x] **Step 4: Re-run migration safety tests and confirm they pass.**

### Task 3: Docker Metadata And Healthcheck

**Files:**
- Modify: `Dockerfile`
- Modify: `cmd/bear/main.go`
- Test: `cmd/bear/main_test.go`
- Test: `scripts/release_check_test.go`

- [x] **Step 1: Add failing tests for OCI labels and `HEALTHCHECK` in root and generated Dockerfiles.**
- [x] **Step 2: Run `GOPROXY=https://goproxy.cn,direct go test ./cmd/bear ./scripts -run 'Dockerfile|Docker' -count=1` and confirm failure.**
- [x] **Step 3: Add OCI labels and healthcheck.**
- [x] **Step 4: Re-run Dockerfile tests and confirm they pass.**

### Task 4: Dependabot, Security Policy, Runbook

**Files:**
- Create: `.github/dependabot.yml`
- Create: `SECURITY.md`
- Create: `docs/runbook.md`
- Test: `scripts/release_check_test.go`

- [x] **Step 1: Add failing repository metadata tests.**
- [x] **Step 2: Run `GOPROXY=https://goproxy.cn,direct go test ./scripts -run 'Dependabot|SecurityPolicy|Runbook' -count=1` and confirm failure.**
- [x] **Step 3: Add Dependabot config, security policy, and runbook.**
- [x] **Step 4: Re-run metadata tests and confirm they pass.**

### Task 5: Full Verification And Commit

- [x] **Step 1: Run `GOPROXY=https://goproxy.cn,direct go build ./cmd ./cmd/bear ./cmd/bear-cli`.**
- [x] **Step 2: Run `GOPROXY=https://goproxy.cn,direct go test ./... -count=1`.**
- [x] **Step 3: Run `GOPROXY=https://goproxy.cn,direct go test -race ./... -count=1`.**
- [x] **Step 4: Run `GOPROXY=https://goproxy.cn,direct go vet ./...`.**
- [x] **Step 5: Run `GOPROXY=https://goproxy.cn,direct $(go env GOPATH)/bin/govulncheck ./...`.**
- [x] **Step 6: Run `GOPROXY=https://goproxy.cn,direct scripts/release-check.sh`.**
- [x] **Step 7: Try `docker build .` and record Docker daemon status.**
- [ ] **Step 8: Commit with a complete Chinese message and push `codex/production-baseline`.**

# Production SQL Migrations Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Provide an explicit SQL migration runner for production deploy workflows.

**Architecture:** Add dependency-free migration loading and execution in `pkg/bear`. The runner accepts `*sql.DB`, stores applied versions in `schema_migrations`, and runs migrations transactionally. Documentation shows how to call it from a deploy step.

**Tech Stack:** Go, database/sql, GORM adapter interoperability, SQL files.

---

### Task 1: Migration Tests

**Files:**
- Modify: `pkg/bear/production_baseline_test.go`

- [x] Write a failing test for loading sorted `*.up.sql` files and applying them to an in-memory database.
- [x] Write a failing test for idempotent re-runs.
- [x] Write a failing test for invalid SQL stopping with an error.
- [x] Run: `GOPROXY=https://goproxy.cn,direct go test ./pkg/bear -count=1`
- [x] Expected before implementation: tests fail because migration APIs are not implemented.

### Task 2: Migration Runner

**Files:**
- Create: `pkg/bear/migration.go`

- [x] Define `Migration` and `MigrationRunner`.
- [x] Implement `LoadSQLMigrations(dir string) ([]Migration, error)`.
- [x] Implement `NewMigrationRunner(db *sql.DB) *MigrationRunner`.
- [x] Implement `MigrationRunner.Up(ctx context.Context, migrations []Migration) error`.
- [x] Run: `GOPROXY=https://goproxy.cn,direct go test ./pkg/bear -count=1`
- [x] Expected after implementation: package tests pass.

### Task 3: Docs And Verification

**Files:**
- Modify: `docs/production.md`

- [x] Document migration file naming and runner usage.
- [x] Run: `gofmt` on changed Go files.
- [x] Run: `GOPROXY=https://goproxy.cn,direct go build ./cmd ./cmd/bear ./cmd/bear-cli`
- [x] Run: `GOPROXY=https://goproxy.cn,direct go test ./... -count=1`
- [x] Run: `GOPROXY=https://goproxy.cn,direct go test -race ./... -count=1`
- [x] Run: `GOPROXY=https://goproxy.cn,direct go vet ./...`
- [x] Run: `GOPROXY=https://goproxy.cn,direct govulncheck ./...`
- [x] Commit with a detailed Chinese commit message.

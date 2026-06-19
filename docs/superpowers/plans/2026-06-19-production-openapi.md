# Production OpenAPI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Enrich generated OpenAPI documents with path parameters, query parameters, request body schemas, and response schemas.

**Architecture:** Extend `pkg/bear/openapi.go` with small reflection helpers. Keep schemas inline and dependency-free. Cover behavior through focused package tests using the existing `Bear` route registration flow.

**Tech Stack:** Go, Gin, OpenAPI 3.0 JSON.

---

### Task 1: OpenAPI Tests

**Files:**
- Modify: `pkg/bear/production_baseline_test.go`

- [x] Write a failing test for a route with `uri`, `form`, and `json` request tags plus a typed response.
- [x] Run: `GOPROXY=https://goproxy.cn,direct go test ./pkg/bear -count=1`
- [x] Expected before implementation: test fails because the document lacks `{id}`, parameters, request body, and response schema.

### Task 2: OpenAPI Generator

**Files:**
- Modify: `pkg/bear/openapi.go`

- [x] Convert Gin paths to OpenAPI path syntax.
- [x] Generate path/query parameters from request struct tags.
- [x] Generate request body schema from JSON-tagged fields.
- [x] Generate response schema from first non-error return value.
- [x] Run: `GOPROXY=https://goproxy.cn,direct go test ./pkg/bear -count=1`
- [x] Expected after implementation: package tests pass.

### Task 3: Docs And Verification

**Files:**
- Modify: `docs/production.md`

- [x] Document Swagger/OpenAPI production usage and limitations.
- [x] Run: `gofmt` on changed Go files.
- [x] Run: `GOPROXY=https://goproxy.cn,direct go build ./cmd ./cmd/bear ./cmd/bear-cli`
- [x] Run: `GOPROXY=https://goproxy.cn,direct go test ./... -count=1`
- [x] Run: `GOPROXY=https://goproxy.cn,direct go test -race ./... -count=1`
- [x] Run: `GOPROXY=https://goproxy.cn,direct go vet ./...`
- [x] Run: `GOPROXY=https://goproxy.cn,direct govulncheck ./...`
- [x] Commit with a detailed Chinese commit message.

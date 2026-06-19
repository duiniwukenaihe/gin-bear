# Production Delivery Gates Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add CI delivery gates and clean deprecated old CLI generation code.

**Architecture:** Keep CI checks in `.github/workflows/ci.yml`, keep old CLI helper code local to `cmd/bear`, and cover behavior with a focused package test. Verification is performed with Go tests, race tests, vet, vulnerability scanning, and Docker build where the environment supports it.

**Tech Stack:** GitHub Actions, Go test/vet/race, govulncheck, Docker.

---

### Task 1: Old CLI Title Helper Test

**Files:**
- Create: `cmd/bear/main_test.go`
- Modify: `cmd/bear/main.go`

- [x] Write tests for converting `user`, `user_profile`, and `user-profile` into exported Go identifiers.
- [x] Run: `GOPROXY=https://goproxy.cn,direct go test ./cmd/bear -count=1`
- [x] Expected before implementation: build fails because helper is not implemented.
- [x] Replace deprecated `strings.Title` use with the helper.
- [x] Run: `GOPROXY=https://goproxy.cn,direct go test ./cmd/bear -count=1`
- [x] Expected after implementation: package tests pass.

### Task 2: CI Quality Gates

**Files:**
- Modify: `.github/workflows/ci.yml`
- Modify: `docs/production.md`

- [x] Add explicit command package build.
- [x] Add race test step.
- [x] Add `govulncheck` install and scan step.
- [x] Document local delivery verification commands.
- [x] Run local equivalents where possible.

### Task 3: Full Verification And Commit

**Files:**
- All changed files

- [x] Run: `gofmt` on changed Go files.
- [x] Run: `GOPROXY=https://goproxy.cn,direct go test ./... -count=1`
- [x] Run: `GOPROXY=https://goproxy.cn,direct go test -race ./... -count=1`
- [x] Run: `GOPROXY=https://goproxy.cn,direct go vet ./...`
- [x] Run: `GOPROXY=https://goproxy.cn,direct govulncheck ./...`
- [x] Run: `docker build .` if Docker daemon is available. Local Docker daemon was unavailable.
- [x] Commit with a detailed Chinese commit message.

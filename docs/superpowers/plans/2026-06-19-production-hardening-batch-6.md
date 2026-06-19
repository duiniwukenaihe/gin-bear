# Production Hardening Batch 6 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add Kubernetes deployment assets, Prometheus alert rules, and documentation so the scaffold has a practical production deployment starting point.

**Architecture:** Keep deploy assets under `deploy/` and verify them with repository tests in `scripts/release_check_test.go`. Do not add runtime dependencies.

**Tech Stack:** Kubernetes YAML, PrometheusRule YAML, Go repository tests.

---

### Task 1: Deployment Asset Tests

**Files:**
- Modify: `scripts/release_check_test.go`

- [x] **Step 1: Add failing tests for Kubernetes manifests and Prometheus rules.**
- [x] **Step 2: Run `GOPROXY=https://goproxy.cn,direct go test ./scripts -run 'Kubernetes|Prometheus' -count=1` and confirm failure.**

### Task 2: Kubernetes Manifests

**Files:**
- Create: `deploy/kubernetes/configmap.yaml`
- Create: `deploy/kubernetes/deployment.yaml`
- Create: `deploy/kubernetes/service.yaml`
- Create: `deploy/kubernetes/hpa.yaml`
- Create: `deploy/kubernetes/pdb.yaml`
- Modify: `.dockerignore`

- [x] **Step 1: Add base Kubernetes manifests.**
- [x] **Step 2: Re-run Kubernetes manifest tests and confirm they pass.**

### Task 3: Prometheus Rules And Docs

**Files:**
- Create: `deploy/prometheus/rules.yaml`
- Modify: `docs/production.md`
- Modify: `docs/runbook.md`

- [x] **Step 1: Add alert rules and link deploy assets from docs.**
- [x] **Step 2: Re-run Prometheus/docs tests and confirm they pass.**

### Task 4: Full Verification And Commit

- [x] **Step 1: Run `GOPROXY=https://goproxy.cn,direct go build ./cmd ./cmd/bear ./cmd/bear-cli`.**
- [x] **Step 2: Run `GOPROXY=https://goproxy.cn,direct go test ./... -count=1`.**
- [x] **Step 3: Run `GOPROXY=https://goproxy.cn,direct go test -race ./... -count=1`.**
- [x] **Step 4: Run `GOPROXY=https://goproxy.cn,direct go vet ./...`.**
- [x] **Step 5: Run `GOPROXY=https://goproxy.cn,direct $(go env GOPATH)/bin/govulncheck ./...`.**
- [x] **Step 6: Run `GOPROXY=https://goproxy.cn,direct scripts/release-check.sh`.**
- [x] **Step 7: Try `docker build .` and record Docker daemon status.**
- [ ] **Step 8: Commit with a complete Chinese message and push `codex/production-baseline`.**

# Production Readiness Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Complete the next production-readiness layer for gin-bear.

**Architecture:** Keep runtime changes in `pkg/bear`, deployment files at the repository root, CI in `.github/workflows`, and generated-project defaults inside CLI scaffolding. Behavior changes are covered by Go tests; pure config/artifact additions are verified by full repository tests and file review.

**Tech Stack:** Go, Gin, GORM, go-redis, Docker multi-stage builds, Docker Compose, GitHub Actions.

---

## File Structure

- Modify: `.gitignore` to ignore root binaries without hiding source directories.
- Modify: `pkg/bear/config.go` for runtime, auth, JWT, and DB logging config.
- Modify: `pkg/bear/bear.go` for Gin runtime setup and production security validation.
- Modify: `pkg/bear/health.go` for `/health`, `/live`, `/ready`.
- Modify: `pkg/bear/db.go` and `pkg/bear/redis.go` for readiness checks.
- Modify: `pkg/bear/jwt.go` and `pkg/bear/jwt_fairing.go` for stronger JWT and public path config.
- Modify: `cmd/bear-cli/cmd/new.go` for generated project artifacts.
- Modify: `README.md` and `cmd/bear-cli/cmd/gen.go` to align validation tags.
- Create: `.env.example`, `application-prod.yaml.example`, `Dockerfile`, `docker-compose.yml`, `.github/workflows/ci.yml`.
- Extend: `pkg/bear/production_baseline_test.go`.

---

### Task 1: Repository and Deployment Artifacts

- [ ] Fix `.gitignore` root binary patterns.
- [ ] Add `.env.example`.
- [ ] Add `application-prod.yaml.example`.
- [ ] Add `Dockerfile`.
- [ ] Add `docker-compose.yml`.
- [ ] Add `.github/workflows/ci.yml`.

### Task 2: Runtime Mode, Trusted Proxies, and Production Secret Guard

- [ ] Write failing tests for production weak JWT secret rejection and Gin runtime mode setup.
- [ ] Add `server.mode` and runtime helpers.
- [ ] Reject weak JWT secrets only in production.
- [ ] Apply trusted proxies from config.
- [ ] Run focused tests.

### Task 3: Liveness and Readiness

- [ ] Write failing tests for `/live` success, `/ready` success, and `/ready` dependency failure.
- [ ] Add readiness checker interfaces.
- [ ] Implement DB and Redis readiness checks.
- [ ] Update `HealthController`.
- [ ] Run focused tests.

### Task 4: Auth Public Path Config and JWT Validation

- [ ] Write failing tests for configurable auth public paths.
- [ ] Add `auth.public_paths`.
- [ ] Replace hard-coded demo auth skips with config-driven matching.
- [ ] Validate JWT signing method on parse.
- [ ] Run focused tests.

### Task 5: Generator and Template Alignment

- [ ] Update README validation examples from `validate` to `binding`.
- [ ] Ensure generated DTOs use `binding`.
- [ ] Upgrade `bear new` generated project with config examples and Docker artifacts.
- [ ] Run full tests.

### Task 6: Final Verification

- [ ] Run `GOPROXY=https://goproxy.cn,direct go test ./... -count=1`.
- [ ] Run `GOPROXY=https://goproxy.cn,direct go vet ./...`.
- [ ] Review `git diff --stat`.

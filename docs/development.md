# Development guide

Single source of truth for working in this repository. Tool-specific local
rules live in `AGENTS.md`/`agent.md` (never committed); everything committable
and versioned is here.

## Commands

All commands assume the local toolchain from `AGENTS.md`:

```sh
export PATH="/opt/homebrew/bin:$PATH"
export GOPROXY=https://goproxy.cn,direct
export GOTOOLCHAIN=go1.25.14
```

| Task | Command |
| --- | --- |
| Unit tests (repeatable) | `go test ./... -count=1` |
| Full gate | `RC_ALLOW_NETWORK=1 API_COMPAT_ALLOW_NETWORK=1 make verify` |
| Race | `go test -race ./... -count=1` |
| Real-dependency acceptance | `scripts/test-integration.sh` |
| Diagnose a project | `go run ./cmd/bear doctor [--format json] [--probe]` |
| Scaffold / generate | `go run ./cmd/bear new`, `bear gen api <name>` |

`make verify` covers tests, coverage, v0.9.1 API compatibility, generated-app
E2E, race, vet, staticcheck, and govulncheck. `scripts/test-integration.sh`
needs PostgreSQL/Redis (MySQL optional) and cleans up its disposable
databases itself.

## Directory map

| Path | Owns |
| --- | --- |
| `pkg/bear` | Framework runtime: lifecycle, config, GORM, auth, Casbin, migration runner, observability |
| `internal/cli` | `bear` commands (`new`, `gen`, `doctor`, `agent`); owns generation orchestration |
| `internal/scaffold` | Project templates, manifest, generation integration tests |
| `internal/atomicdir` | Atomic publish helper shared by generators |
| `tests/integration` | Real-dependency acceptance (env-gated, skipped by default) |
| `scripts/` | CI diagnostics, release check, integration entrypoint |
| `docs/` | Versioned engineering docs (this file, `architecture.md`, `recipes/`) |
| `extensions/`, `tools/` | Opt-in nested modules with their own `go.mod` and CI |

## Dependency rules

- Root module dependencies serve the runtime and CLI only. Nested modules
  (`extensions/*`, `tools/*`) declare their own `go.mod` and are tested by
  their own CI jobs; root `go test ./...` never covers them.
- Prefer the standard library and existing dependencies over new ones.
- Never `git add -f` ignored local files (`AGENTS.md`, `agent.md`, `.env`).

## Anti-patterns

- Rewriting all generation templates to "fix" one dialect bug: fix the
  dialect mapping (`migrationDialect`/`columnSQLType`) and add a case to
  `TestCreateTableSQLMatchesConfiguredDialect`.
- Globally enabling Gin `ContextWithFallback` to fix context propagation:
  fix it at the `Repository.DB` boundary so user handlers keep their semantics.
- String-matching templates to prove behavior: execute the generated code
  (see `internal/scaffold/generated_migration_test.go` and the chain test).
- Claiming cluster-wide revocation from a local test: revocation is local
  until every instance confirms `LoadPolicy` (see `docs/production.md`).

## Task card template

Every change ships with: goal, non-goals, files to read, files to change,
acceptance (commands plus expected output), rollback, and the actual evidence
collected. One work package per diff; cross-package refactors are split and
justified.

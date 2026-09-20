#!/usr/bin/env bash
# W1 integration entrypoint: one command for a repeatable run.
#
#   scripts/test-integration.sh
#
# Flow: prepare disposable credentials -> wait for readiness -> run the
# tests -> collect a redacted log -> clean up everything this run created.
#
# Safety rules (hard):
# - Only databases/roles named bear_integration_* are ever created or dropped.
#   Development and production databases are never touched.
# - No host-PID mounts, no public ports; compose binds loopback only.
# - Credentials never appear in output: the log is redacted before it is
#   written or printed.
#
# Inputs (all optional):
#   BEAR_INTEGRATION_PG_DSN     explicit postgres DSN (CI services); when unset
#                               and a local server answers, a disposable role +
#                               database is created automatically.
#   BEAR_INTEGRATION_MYSQL_DSN  explicit mysql DSN; when unset, MySQL subtests
#                               report NOT_RUN instead of passing.
#   BEAR_INTEGRATION_REDIS_ADDR redis addr, default 127.0.0.1:6379; when the
#                               server is unreachable, Redis subtests NOT_RUN.
#   BEAR_INTEGRATION_KEEP_DB=1  keep the disposable database for inspection.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

export PATH="/opt/homebrew/bin:$PATH"
# Go caches: explicit environment wins; otherwise use this machine's own
# defaults. The Mac absolute paths below apply only to the maintainer host
# and never propagate to other runners.
if [ -z "${GOCACHE:-}" ]; then
  if [ "$(uname)" = "Darwin" ] && [ "${USER:-}" = "zhangpeng" ]; then
    GOCACHE="/Users/zhangpeng/Library/Caches/go-build"
  else
    GOCACHE="$(go env GOCACHE)"
  fi
fi
if [ -z "${GOMODCACHE:-}" ]; then
  if [ "$(uname)" = "Darwin" ] && [ "${USER:-}" = "zhangpeng" ]; then
    GOMODCACHE="/Users/zhangpeng/go/pkg/mod"
  else
    GOMODCACHE="$(go env GOMODCACHE)"
  fi
fi
export GOCACHE GOMODCACHE
export GOPROXY="${GOPROXY:-https://goproxy.cn,direct}"
export GOSUMDB="${GOSUMDB:-sum.golang.org}"
export GOTOOLCHAIN="${GOTOOLCHAIN:-go1.26.6}"

# Disk gate: refuse heavy integration builds below 10 GiB free.
free_kb() {
  if command -v df >/dev/null 2>&1; then
    df -k / | awk 'NR==2 {print $4}'
  else
    echo "0"
  fi
}
if [ "$(free_kb)" -lt 10485760 ]; then
  echo "refusing integration run: less than 10 GiB free on /" >&2
  exit 2
fi

STAMP="$(date +%Y%m%d%H%M%S)_$$"
PG_ROLE="bear_integration_${STAMP}"
PG_DB="bear_integration_${STAMP}"
PG_PASSWORD="pw_${STAMP}_int"
CREATED_ROLE=0
CREATED_DB=0
ADMIN_PS=""

redact() {
  # DSN userinfo (user:password@) and password= / password: assignments.
  sed -E -e 's#://[^/@]*@#://***@#g' -e 's#([Pp][Aa][Ss][Ss][Ww][Oo][Rr][Dd]["=:> ]+)[^" &,;]+#\1***#g'
}

cleanup() {
  if [ "${BEAR_INTEGRATION_KEEP_DB:-0}" = "1" ]; then
    echo "BEAR_INTEGRATION_KEEP_DB=1: keeping ${PG_ROLE}/${PG_DB} for inspection" | redact
    return
  fi
  if [ "$CREATED_DB" = "1" ]; then
    $ADMIN_PS -d postgres -c "DROP DATABASE IF EXISTS \"${PG_DB}\";" >/dev/null 2>&1 || true
  fi
  if [ "$CREATED_ROLE" = "1" ]; then
    $ADMIN_PS -d postgres -c "DROP ROLE IF EXISTS \"${PG_ROLE}\";" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

if [ -z "${BEAR_INTEGRATION_PG_DSN:-}" ]; then
  if command -v pg_isready >/dev/null 2>&1 && pg_isready -q -h /tmp -p 5432 2>/dev/null; then
    ADMIN_PS="psql -h /tmp -U zhangpeng"
    $ADMIN_PS -d postgres -c "CREATE ROLE \"${PG_ROLE}\" LOGIN PASSWORD '${PG_PASSWORD}' CREATEDB;" >/dev/null
    CREATED_ROLE=1
    $ADMIN_PS -d postgres -c "CREATE DATABASE \"${PG_DB}\" OWNER \"${PG_ROLE}\";" >/dev/null
    CREATED_DB=1
    $ADMIN_PS -d postgres -c "GRANT pg_signal_backend TO \"${PG_ROLE}\";" >/dev/null
    export BEAR_INTEGRATION_PG_DSN="postgres://${PG_ROLE}:${PG_PASSWORD}@127.0.0.1:5432/${PG_DB}?sslmode=disable"
    echo "prepared disposable postgres ${PG_DB} (role ${PG_ROLE})"
  else
    echo "no BEAR_INTEGRATION_PG_DSN and no local postgres: PG subtests will NOT_RUN"
  fi
fi

if [ -z "${BEAR_INTEGRATION_MYSQL_DSN:-}" ]; then
  echo "no BEAR_INTEGRATION_MYSQL_DSN: MySQL subtests will NOT_RUN"
fi

if [ -z "${BEAR_INTEGRATION_REDIS_ADDR:-}" ]; then
  export BEAR_INTEGRATION_REDIS_ADDR="127.0.0.1:6379"
fi

export BEAR_INTEGRATION=1
LOG="$(mktemp -t bear-integration-XXXXXX.log)"
echo "integration log: ${LOG}"
# GOTMPDIR/TMPDIR stay outside the repo per local build rules.
GOTMPDIR="$(mktemp -d)"
export GOTMPDIR
export TMPDIR="$GOTMPDIR"

cleanup_tmp() {
  rm -rf "$GOTMPDIR"
}
trap 'cleanup; cleanup_tmp' EXIT

set +e
go test ./tests/integration/ -count=1 -v 2>&1 | redact | tee "$LOG"
STATUS=${PIPESTATUS[0]}
set -e
cleanup_tmp
trap cleanup EXIT

if [ "$STATUS" -ne 0 ]; then
  echo "integration FAILED (log: ${LOG})"
  exit "$STATUS"
fi
echo "integration PASSED (log: ${LOG})"

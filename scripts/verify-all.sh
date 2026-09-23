#!/usr/bin/env bash
# Single quality entry point for `make verify`.
#
# The root module gate (release-check.sh) never descends into the nested
# modules (extensions/agent, tools/bear-mcp) because `go test ./...` stops at a
# nested go.mod. Run both gates here so one command covers every shipped module.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

scripts/release-check.sh
scripts/verify-modules.sh

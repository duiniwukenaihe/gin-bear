#!/usr/bin/env bash
# Nested-module quality gate.
#
# extensions/agent and tools/bear-mcp are separate Go modules. The root
# `go test ./...` never descends into a nested module, so without this script a
# release could ship a broken agent or MCP bridge. Run the same quality steps
# for each nested module here so `make verify` and CI cover all shipped code.
#
# Network mode follows release-check.sh: RC_ALLOW_NETWORK=0 is offline and
# requires controlled tool binaries; RC_ALLOW_NETWORK=1 may `go run` pinned
# tools.
#
# Inputs:
#   RC_ALLOW_NETWORK              0 (offline, default) or 1 (online opt-in)
#   STATICCHECK_BIN               absolute pinned staticcheck binary
#   STATICCHECK_EXPECTED_SHA256   required when STATICCHECK_BIN is set
#   GOVULNCHECK_BIN               absolute pinned govulncheck binary
#   GOVULNCHECK_EXPECTED_SHA256   required when GOVULNCHECK_BIN is set
#   BEAR_AGENT_PG_DSN             when set, the agent task PostgreSQL variant
#                                 runs instead of skipping (CI provides it)
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

MODULES=(extensions/agent tools/bear-mcp)

network_flag="${RC_ALLOW_NETWORK-0}"
case "${network_flag}" in
0)
	export GOPROXY=off
	export GOSUMDB=off
	export GOTOOLCHAIN=local
	;;
1)
	export GOPROXY="${GOPROXY:-https://proxy.golang.org,direct}"
	export GOSUMDB="${GOSUMDB:-sum.golang.org}"
	export GOTOOLCHAIN="${GOTOOLCHAIN:-go1.26.6}"
	;;
*)
	printf 'RC_ALLOW_NETWORK must be 0 or 1\n' >&2
	exit 1
	;;
esac

# Go caches: explicit environment wins; otherwise use this host's own defaults.
# The Mac absolute paths apply only to the maintainer host and never propagate
# to other runners.
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

# Disk gate: refuse heavy nested builds below 10 GiB free.
free_kb() {
	if command -v df >/dev/null 2>&1; then
		df -k / | awk 'NR==2 {print $4}'
	else
		echo "0"
	fi
}
if [ "$(free_kb)" -lt 10485760 ]; then
	echo "refusing nested-module verification: less than 10 GiB free on /" >&2
	exit 2
fi

BUILD_TMP="$(mktemp -d "${TMPDIR:-/tmp}/gin-bear-modules.XXXXXX")"
cleanup() { rm -rf "${BUILD_TMP}"; }
trap cleanup EXIT
export GOTMPDIR="${BUILD_TMP}/gotmp"
export TMPDIR="${BUILD_TMP}/tmp"
mkdir -p "${GOTMPDIR}" "${TMPDIR}"

sha256_file() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{print $1}'
	else
		shasum -a 256 "$1" | awk '{print $1}'
	fi
}

# resolve_tool prints "binary:<path>" for a controlled binary or
# "go-run:<module>" for the online pinned `go run` path. Offline without a
# controlled binary is an error, never a silent skip.
resolve_tool() {
	local tool_id="$1" bin_env="$2" sha_env="$3" pinned="$4"
	local configured="${!bin_env:-}"
	if [ -n "${configured}" ]; then
		if [ "${configured}" != /* ] || [ ! -f "${configured}" ] || [ -L "${configured}" ] || [ ! -x "${configured}" ]; then
			printf '%s must name an absolute, non-symlink regular executable file: %s\n' "${bin_env}" "${configured}" >&2
			return 1
		fi
		local expected="${!sha_env:-}"
		if ! [[ "${expected}" =~ ^[[:xdigit:]]{64}$ ]]; then
			printf '%s must provide a 64-character SHA-256 digest for %s\n' "${sha_env}" "${bin_env}" >&2
			return 1
		fi
		expected="$(printf '%s' "${expected}" | tr '[:upper:]' '[:lower:]')"
		local actual
		actual="$(sha256_file "${configured}")" || return 1
		if [ "${actual}" != "${expected}" ]; then
			printf '%s SHA-256 mismatch for %s: expected %s, got %s\n' "${bin_env}" "${tool_id}" "${expected}" "${actual}" >&2
			return 1
		fi
		printf 'binary:%s\n' "${configured}"
		return 0
	fi
	if [ "${network_flag}" = "1" ]; then
		printf 'go-run:%s\n' "${pinned}"
		return 0
	fi
	printf '%s is required in offline mode; set %s and independently trusted %s\n' "${tool_id}" "${bin_env}" "${sha_env}" >&2
	return 1
}

STATICCHECK_RESOLVED="$(resolve_tool staticcheck STATICCHECK_BIN STATICCHECK_EXPECTED_SHA256 honnef.co/go/tools/cmd/staticcheck@v0.7.0)" || exit $?
GOVULNCHECK_RESOLVED="$(resolve_tool govulncheck GOVULNCHECK_BIN GOVULNCHECK_EXPECTED_SHA256 golang.org/x/vuln/cmd/govulncheck@v1.6.0)" || exit $?

run_tool() {
	local resolved="$1"
	shift
	case "${resolved}" in
	binary:*) "${resolved#binary:}" "$@" ;;
	go-run:*) go run "${resolved#go-run:}" "$@" ;;
	esac
}

status=0
for module in "${MODULES[@]}"; do
	if [ ! -f "${module}/go.mod" ]; then
		printf 'nested module not found: %s/go.mod\n' "${module}" >&2
		status=1
		continue
	fi
	printf '\n==> [%s] go test ./... -count=1\n' "${module}"
	(cd "${module}" && go test ./... -count=1) || status=1
	printf '\n==> [%s] go test -race ./... -count=1\n' "${module}"
	(cd "${module}" && go test -race ./... -count=1) || status=1
	printf '\n==> [%s] go vet ./...\n' "${module}"
	(cd "${module}" && go vet ./...) || status=1
	printf '\n==> [%s] staticcheck ./...\n' "${module}"
	(cd "${module}" && run_tool "${STATICCHECK_RESOLVED}" ./...) || status=1
	printf '\n==> [%s] govulncheck ./...\n' "${module}"
	(cd "${module}" && run_tool "${GOVULNCHECK_RESOLVED}" ./...) || status=1
done

if [ "${status}" -ne 0 ]; then
	echo "nested-module verification FAILED" >&2
	exit 1
fi
echo "nested-module verification PASSED"

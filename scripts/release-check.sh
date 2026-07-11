#!/usr/bin/env bash
set -euo pipefail

network_flag="${RC_ALLOW_NETWORK-0}"
case "${network_flag}" in
0)
	network_mode="offline"
	export GOPROXY=off
	export GOSUMDB=off
	export GOTOOLCHAIN=local
	;;
1)
	network_mode="online-opt-in"
	export GOPROXY="${GOPROXY:-https://proxy.golang.org,direct}"
	export GOSUMDB="${GOSUMDB:-sum.golang.org}"
	export GOTOOLCHAIN="${GOTOOLCHAIN:-go1.25.12}"
	;;
*)
	printf 'RC_ALLOW_NETWORK must be 0 or 1\n' >&2
	exit 1
	;;
esac

if [[ "${RELEASE_CHECK_METADATA+x}" == "x" ]]; then
	if [[ -z "${RELEASE_CHECK_METADATA}" ]]; then
		printf 'RELEASE_CHECK_METADATA must not be empty when explicitly set\n' >&2
		exit 1
	fi
	release_check_metadata="${RELEASE_CHECK_METADATA}"
	mkdir -p "$(dirname "${release_check_metadata}")"
	: >>"${release_check_metadata}"
else
	release_check_metadata="$(mktemp "${TMPDIR:-/tmp}/gin-bear-release-check-metadata.XXXXXX")"
fi

record_release_evidence() {
	local line
	for line in \
		"release_check_network=${network_mode}" \
		"release_check_network_opt_in=${network_flag}" \
		"release_check_metadata=${release_check_metadata}"; do
		printf '%s\n' "${line}"
		printf '%s\n' "${line}" >>"${release_check_metadata}"
	done
}
record_release_evidence

tool_command() {
	local env_name="$1"
	local command_name="$2"
	local pinned_module="$3"
	local configured="${!env_name:-}"
	if [[ -n "${configured}" ]]; then
		if [[ ! -x "${configured}" ]]; then
			printf '%s must name an executable file: %s\n' "${env_name}" "${configured}" >&2
			return 1
		fi
		resolved_command=("${configured}")
		return 0
	fi
	local installed
	if installed="$(command -v "${command_name}")"; then
		resolved_command=("${installed}")
	elif [[ "${network_flag}" == "1" ]]; then
		resolved_command=(go run "${pinned_module}")
	else
		printf '%s is required in offline mode; preinstall it or set %s\n' "${command_name}" "${env_name}" >&2
		return 1
	fi
}

resolved_command=()
tool_command STATICCHECK_BIN staticcheck honnef.co/go/tools/cmd/staticcheck@v0.7.0 || exit $?
staticcheck_command=("${resolved_command[@]}")
tool_command GOVULNCHECK_BIN govulncheck golang.org/x/vuln/cmd/govulncheck@v1.6.0 || exit $?
govulncheck_command=("${resolved_command[@]}")
govulncheck_scan_args=()
if [[ "${network_flag}" == "0" ]]; then
	if [[ -z "${GOVULNCHECK_DB:-}" ]]; then
		printf 'GOVULNCHECK_DB is required in offline mode and must point to a local vulnerability database\n' >&2
		exit 1
	fi
	govulncheck_scan_args=(-db "${GOVULNCHECK_DB}")
fi

release_coverage_minimum="70.0"
release_critical_coverage_minimum="80.0"

BUILD_DIR=""
coverage_profile=""
coverage_profile_owned=false
cleanup() {
	if [[ -n "${BUILD_DIR}" ]]; then
		rm -rf "${BUILD_DIR}"
	fi
	if [[ "${coverage_profile_owned}" == "true" && -n "${coverage_profile}" ]]; then
		rm -f "${coverage_profile}"
	fi
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

if [[ "${COVERAGE_PROFILE+x}" == "x" ]]; then
	if [[ -z "${COVERAGE_PROFILE}" ]]; then
		printf 'COVERAGE_PROFILE must not be empty when explicitly set\n' >&2
		exit 1
	fi
	coverage_profile="${COVERAGE_PROFILE}"
	coverage_profile_owned=false
	printf '==> Using caller-owned coverage profile %s\n' "${coverage_profile}"
else
	coverage_profile="$(mktemp "${TMPDIR:-/tmp}/gin-bear-coverage.XXXXXX")"
	coverage_profile_owned=true
	printf '==> Using release-owned temporary coverage profile %s\n' "${coverage_profile}"
fi
BUILD_DIR="$(mktemp -d "${TMPDIR:-/tmp}/gin-bear-build.XXXXXX")"

echo "==> Checking module tidiness"
go mod tidy -diff
git diff --check

echo "==> Building command packages"
go build ./cmd ./cmd/bear ./cmd/bear-cli

echo "==> Building main app with release metadata"
MODULE_PATH="$(go list -m)"
VERSION="${VERSION:-dev}"
COMMIT="${COMMIT:-$(git rev-parse --short HEAD 2>/dev/null || echo unknown)}"
BUILD_TIME="${BUILD_TIME:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"
go build \
	-ldflags="-X ${MODULE_PATH}/pkg/bear.Version=${VERSION} -X ${MODULE_PATH}/pkg/bear.Commit=${COMMIT} -X ${MODULE_PATH}/pkg/bear.BuildTime=${BUILD_TIME}" \
	-o "${BUILD_DIR}/gin-bear" \
	./cmd

echo "==> Running tests"
go test ./... -count=1

echo "==> Measuring repository and critical-chain coverage"
go test ./... -count=1 -coverprofile="${coverage_profile}"
printf '==> Enforcing release coverage thresholds: total %s%%, critical %s%%\n' "${release_coverage_minimum}" "${release_critical_coverage_minimum}"
env COVERAGE_MINIMUM="${release_coverage_minimum}" CRITICAL_COVERAGE_MINIMUM="${release_critical_coverage_minimum}" \
	scripts/check-coverage.sh "${coverage_profile}"

echo "==> Checking v0.9.1 public API compatibility"
API_COMPAT_METADATA="${API_COMPAT_METADATA:-${release_check_metadata}}" scripts/check-api-compat.sh

echo "==> Running legacy and generated application E2E checks"
BEAR_RELEASE_E2E=1 go test ./scripts/releasee2e -run '^TestReleaseCandidateApplications$' -count=1

echo "==> Running race tests"
go test -race ./... -count=1

echo "==> Running go vet"
go vet ./...

echo "==> Running staticcheck"
"${staticcheck_command[@]}" ./...

echo "==> Running govulncheck"
"${govulncheck_command[@]}" "${govulncheck_scan_args[@]}" ./...

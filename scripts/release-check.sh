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

record_tool_evidence() {
	local line
	for line in "$@"; do
		printf '%s\n' "${line}"
		printf '%s\n' "${line}" >>"${release_check_metadata}"
	done
}

sha256_file() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{print $1}'
	else
		shasum -a 256 "$1" | awk '{print $1}'
	fi
}

tool_command() {
	local tool_id="$1"
	local env_name="$2"
	local expected_digest_env="$3"
	local command_name="$4"
	local pinned_module="$5"
	local expected_path="$6"
	local expected_module="$7"
	local expected_version="$8"
	local configured="${!env_name:-}"
	if [[ -n "${configured}" ]]; then
		if [[ "${configured}" != /* || ! -f "${configured}" || -L "${configured}" || ! -x "${configured}" ]]; then
			printf '%s must name an absolute, non-symlink regular executable file: %s\n' "${env_name}" "${configured}" >&2
			return 1
		fi
		local expected_digest="${!expected_digest_env:-}"
		if [[ ! "${expected_digest}" =~ ^[[:xdigit:]]{64}$ ]]; then
			printf '%s must provide a 64-character SHA-256 digest for %s\n' "${expected_digest_env}" "${env_name}" >&2
			return 1
		fi
		expected_digest="$(printf '%s' "${expected_digest}" | tr '[:upper:]' '[:lower:]')"
		local actual_digest
		if ! actual_digest="$(sha256_file "${configured}")"; then
			printf 'could not calculate SHA-256 for %s: %s\n' "${env_name}" "${configured}" >&2
			return 1
		fi
		if [[ "${actual_digest}" != "${expected_digest}" ]]; then
			printf '%s SHA-256 mismatch: expected %s, got %s\n' "${expected_digest_env}" "${expected_digest}" "${actual_digest}" >&2
			return 1
		fi
		local build_info
		if ! build_info="$(go version -m "${configured}" 2>&1)"; then
			printf '%s must contain readable Go build metadata: %s\n' "${env_name}" "${build_info}" >&2
			return 1
		fi
		local actual_path actual_module actual_version
		actual_path="$(printf '%s\n' "${build_info}" | awk -F '\t' '$2 == "path" { print $3; exit }')"
		actual_module="$(printf '%s\n' "${build_info}" | awk -F '\t' '$2 == "mod" { print $3; exit }')"
		actual_version="$(printf '%s\n' "${build_info}" | awk -F '\t' '$2 == "mod" { print $4; exit }')"
		if [[ "${actual_path}" != "${expected_path}" || "${actual_module}" != "${expected_module}" || "${actual_version}" != "${expected_version}" ]]; then
			printf '%s Go build identity mismatch: expected path=%s module=%s version=%s; got path=%s module=%s version=%s\n' \
				"${env_name}" "${expected_path}" "${expected_module}" "${expected_version}" "${actual_path:-missing}" "${actual_module:-missing}" "${actual_version:-missing}" >&2
			return 1
		fi
		resolved_command=("${configured}")
		tool_evidence=(
			"${tool_id}_source=controlled-binary"
			"${tool_id}_path=${configured}"
			"${tool_id}_expected_sha256=${expected_digest}"
			"${tool_id}_actual_sha256=${actual_digest}"
			"${tool_id}_build_path=${actual_path}"
			"${tool_id}_build_module=${actual_module}"
			"${tool_id}_build_version=${actual_version}"
			"${tool_id}_build_info=${build_info//$'\n'/; }"
		)
		return 0
	fi
	if [[ "${network_flag}" == "1" ]]; then
		resolved_command=(go run "${pinned_module}")
		tool_evidence=(
			"${tool_id}_source=pinned-go-run"
			"${tool_id}_pinned_module=${pinned_module}"
		)
	else
		printf '%s is required in offline mode; set %s and independently trusted %s\n' "${command_name}" "${env_name}" "${expected_digest_env}" >&2
		return 1
	fi
}

resolved_command=()
tool_evidence=()
tool_command staticcheck STATICCHECK_BIN STATICCHECK_EXPECTED_SHA256 staticcheck honnef.co/go/tools/cmd/staticcheck@v0.7.0 honnef.co/go/tools/cmd/staticcheck honnef.co/go/tools v0.7.0 || exit $?
staticcheck_command=("${resolved_command[@]}")
staticcheck_evidence=("${tool_evidence[@]}")
tool_command govulncheck GOVULNCHECK_BIN GOVULNCHECK_EXPECTED_SHA256 govulncheck golang.org/x/vuln/cmd/govulncheck@v1.6.0 golang.org/x/vuln/cmd/govulncheck golang.org/x/vuln v1.6.0 || exit $?
govulncheck_command=("${resolved_command[@]}")
govulncheck_evidence=("${tool_evidence[@]}")
record_tool_evidence "${staticcheck_evidence[@]}" "${govulncheck_evidence[@]}"
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

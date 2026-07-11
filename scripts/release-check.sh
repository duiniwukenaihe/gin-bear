#!/usr/bin/env bash
set -euo pipefail

export GOSUMDB=sum.golang.org
export GOTOOLCHAIN=go1.25.12
export GOPROXY="${GOPROXY:-https://goproxy.cn,direct}"

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
scripts/check-api-compat.sh

echo "==> Running legacy and generated application E2E checks"
BEAR_RELEASE_E2E=1 go test ./scripts/releasee2e -run '^TestReleaseCandidateApplications$' -count=1

echo "==> Running race tests"
go test -race ./... -count=1

echo "==> Running go vet"
go vet ./...

echo "==> Running staticcheck"
go run honnef.co/go/tools/cmd/staticcheck@v0.7.0 ./...

echo "==> Running govulncheck"
go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...

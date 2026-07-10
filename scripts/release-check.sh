#!/usr/bin/env bash
set -euo pipefail

export GOPROXY="${GOPROXY:-https://goproxy.cn,direct}"
coverage_profile="${COVERAGE_PROFILE:-coverage.out}"
coverage_packages=(
	./cmd/bear-cli/cmd
	./pkg/bear
)

echo "==> Checking module tidiness"
go mod tidy
git diff --exit-code -- go.mod go.sum
git diff --check

echo "==> Building command packages"
go build ./cmd ./cmd/bear ./cmd/bear-cli

echo "==> Building main app with release metadata"
MODULE_PATH="$(go list -m)"
VERSION="${VERSION:-dev}"
COMMIT="${COMMIT:-$(git rev-parse --short HEAD 2>/dev/null || echo unknown)}"
BUILD_TIME="${BUILD_TIME:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"
BUILD_DIR="$(mktemp -d)"
trap 'rm -rf "${BUILD_DIR}"' EXIT
go build \
	-ldflags="-X ${MODULE_PATH}/pkg/bear.Version=${VERSION} -X ${MODULE_PATH}/pkg/bear.Commit=${COMMIT} -X ${MODULE_PATH}/pkg/bear.BuildTime=${BUILD_TIME}" \
	-o "${BUILD_DIR}/gin-bear" \
	./cmd

echo "==> Running tests"
go test ./... -count=1

echo "==> Measuring core package coverage"
go test "${coverage_packages[@]}" -count=1 -coverprofile="${coverage_profile}"
scripts/check-coverage.sh "${coverage_profile}"

echo "==> Running race tests"
go test -race ./... -count=1

echo "==> Running go vet"
go vet ./...

echo "==> Running staticcheck"
go run honnef.co/go/tools/cmd/staticcheck@v0.7.0 ./...

echo "==> Running govulncheck"
go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...

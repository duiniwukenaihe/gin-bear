#!/usr/bin/env bash
set -euo pipefail

export GOPROXY="${GOPROXY:-https://goproxy.cn,direct}"

echo "==> Checking module tidiness"
go mod tidy
git diff --exit-code -- go.mod go.sum

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

echo "==> Running race tests"
go test -race ./... -count=1

echo "==> Running go vet"
go vet ./...

echo "==> Running govulncheck"
GOVULNCHECK_BIN="$(command -v govulncheck || true)"
if [[ -z "${GOVULNCHECK_BIN}" ]]; then
	GOVULNCHECK_BIN="$(go env GOPATH)/bin/govulncheck"
fi
if [[ ! -x "${GOVULNCHECK_BIN}" ]]; then
	go install golang.org/x/vuln/cmd/govulncheck@latest
fi
"${GOVULNCHECK_BIN}" ./...

echo "==> Generating optional SBOM"
if command -v syft >/dev/null 2>&1; then
	syft dir:. -o spdx-json=sbom.spdx.json
else
	echo "syft not found; skipping SBOM generation"
fi

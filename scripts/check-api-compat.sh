#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
baseline="${script_dir}/api/v0.9.1.txt"
module="github.com/duiniwukenaihe/gin-bear"
apidiff="golang.org/x/exp/cmd/apidiff@v0.0.0-20260709172345-9ea1abe57597"

if [[ ! -f "${baseline}" ]]; then
	printf 'v0.9.1 API manifest not found: %s\n' "${baseline}" >&2
	exit 1
fi

if ! incompatible="$(go run "${apidiff}" -m -incompatible "${baseline}" "${module}")"; then
	printf 'v0.9.1 API compatibility analysis failed\n' >&2
	exit 1
fi
if [[ -n "${incompatible}" ]]; then
	printf 'v0.9.1 public API incompatibilities detected:\n%s\n' "${incompatible}" >&2
	exit 1
fi
printf 'v0.9.1 public API compatibility meets the additive-only policy\n'

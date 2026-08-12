#!/usr/bin/env bash
set -uo pipefail

if [[ "$#" -lt 2 ]]; then
	printf 'usage: %s <title> <command> [args...]\n' "$0" >&2
	exit 2
fi

title="$1"
shift
log="$(mktemp "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/gin-bear-ci.XXXXXX")"
trap 'rm -f "${log}"' EXIT

"$@" 2>&1 | tee "${log}"
status="${PIPESTATUS[0]}"
if [[ "${status}" -eq 0 || "${GITHUB_ACTIONS:-}" != "true" ]]; then
	exit "${status}"
fi

details="$(tail -n 80 "${log}")"
details="${details//%/%25}"
details="${details//$'\r'/%0D}"
details="${details//$'\n'/%0A}"
printf '::error title=%s::%s\n' "${title}" "${details}"
exit "${status}"

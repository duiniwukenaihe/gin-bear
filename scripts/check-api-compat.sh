#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
baseline="${script_dir}/api/v0.9.1.txt"
checksum="${script_dir}/api/v0.9.1.txt.sha256"
module="github.com/duiniwukenaihe/gin-bear"
baseline_version="v0.9.1"
apidiff="golang.org/x/exp/cmd/apidiff@v0.0.0-20260709172345-9ea1abe57597"

if [[ ! -f "${baseline}" ]]; then
	printf 'v0.9.1 API manifest not found: %s\n' "${baseline}" >&2
	exit 1
fi
if [[ ! -f "${checksum}" ]]; then
	printf 'v0.9.1 API manifest checksum not found: %s\n' "${checksum}" >&2
	exit 1
fi

expected_sha256="$(awk 'NF == 2 && $2 == "v0.9.1.txt" { print $1 }' "${checksum}")"
if [[ ! "${expected_sha256}" =~ ^[0-9a-f]{64}$ ]]; then
	printf 'v0.9.1 API manifest checksum is malformed: %s\n' "${checksum}" >&2
	exit 1
fi
if command -v sha256sum >/dev/null 2>&1; then
	actual_sha256="$(sha256sum "${baseline}" | awk '{print $1}')"
else
	actual_sha256="$(shasum -a 256 "${baseline}" | awk '{print $1}')"
fi
if [[ "${actual_sha256}" != "${expected_sha256}" ]]; then
	printf 'v0.9.1 API manifest SHA256 mismatch: got %s, want %s\n' "${actual_sha256}" "${expected_sha256}" >&2
	exit 1
fi

rebuild_baseline() {
	local module_json module_dir temporary rebuilt
	module_json="$(go mod download -json "${module}@${baseline_version}")"
	module_dir="$(sed -n 's/^[[:space:]]*"Dir": "\(.*\)",$/\1/p' <<<"${module_json}")"
	if [[ -z "${module_dir}" || ! -d "${module_dir}" ]]; then
		printf 'could not resolve public module cache directory for %s@%s\n' "${module}" "${baseline_version}" >&2
		return 1
	fi
	temporary="$(mktemp -d "${TMPDIR:-/tmp}/gin-bear-api-v091.XXXXXX")"
	rebuilt="${temporary}/v0.9.1.rebuilt.txt"
	cp -R "${module_dir}/." "${temporary}/module"
	chmod -R u+w "${temporary}/module"
	find "${temporary}/module" -type f -name '*.go' -exec \
		sed -i.bak 's#"bear/#"github.com/duiniwukenaihe/gin-bear/#g' {} +
	find "${temporary}/module" -type f -name '*.bak' -delete
	(
		cd "${temporary}/module"
		GOWORK=off go run "${apidiff}" -m -w "${rebuilt}" "${module}"
	)
	if ! cmp -s "${baseline}" "${rebuilt}"; then
		printf 'public %s@%s rebuild does not match committed API baseline\n' "${module}" "${baseline_version}" >&2
		rm -rf "${temporary}"
		return 1
	fi
	rm -rf "${temporary}"
	printf 'v0.9.1 API baseline reproducibly rebuilt from the public module cache\n'
}

rebuild_flag="${API_BASELINE_REBUILD-0}"
case "${rebuild_flag}" in
0) ;;
1) rebuild_baseline ;;
*)
	printf 'API_BASELINE_REBUILD must be 0 or 1\n' >&2
	exit 1
	;;
esac

if ! incompatible="$(go run "${apidiff}" -m -incompatible "${baseline}" "${module}")"; then
	printf 'v0.9.1 API compatibility analysis failed\n' >&2
	exit 1
fi
if [[ -n "${incompatible}" ]]; then
	printf 'v0.9.1 public API incompatibilities detected:\n%s\n' "${incompatible}" >&2
	exit 1
fi
printf 'v0.9.1 public API compatibility meets the additive-only policy\n'

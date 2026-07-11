#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
baseline="${script_dir}/api/v0.9.1.txt"
checksum="${script_dir}/api/v0.9.1.txt.sha256"
module="github.com/duiniwukenaihe/gin-bear"
baseline_version="v0.9.1"
apidiff="golang.org/x/exp/cmd/apidiff@v0.0.0-20260709172345-9ea1abe57597"

network_flag="${API_COMPAT_ALLOW_NETWORK-0}"
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
	;;
*)
	printf 'API_COMPAT_ALLOW_NETWORK must be 0 or 1\n' >&2
	exit 1
	;;
esac

rebuild_flag="${API_BASELINE_REBUILD-0}"
case "${rebuild_flag}" in
0) rebuild_mode="disabled" ;;
1) rebuild_mode="enabled" ;;
*)
	printf 'API_BASELINE_REBUILD must be 0 or 1\n' >&2
	exit 1
	;;
esac

apidiff_command=()
apidiff_path="not-applicable"
apidiff_sha256="not-applicable"
apidiff_go_version_m="not-applicable"
if [[ "${APIDIFF_BIN+x}" == "x" ]]; then
	if [[ -z "${APIDIFF_BIN}" ]]; then
		printf 'APIDIFF_BIN must not be empty when explicitly set\n' >&2
		exit 1
	fi
	if [[ "${APIDIFF_BIN}" != /* ]]; then
		printf 'APIDIFF_BIN must be an absolute path: %s\n' "${APIDIFF_BIN}" >&2
		exit 1
	fi
	if [[ -L "${APIDIFF_BIN}" || ! -f "${APIDIFF_BIN}" || ! -x "${APIDIFF_BIN}" ]]; then
		printf 'APIDIFF_BIN must name an executable file: %s\n' "${APIDIFF_BIN}" >&2
		exit 1
	fi
	apidiff_path="$(cd "$(dirname "${APIDIFF_BIN}")" && pwd -P)/$(basename "${APIDIFF_BIN}")"
	if command -v sha256sum >/dev/null 2>&1; then
		apidiff_sha256="$(sha256sum "${apidiff_path}" | awk '{print $1}')"
	else
		apidiff_sha256="$(shasum -a 256 "${apidiff_path}" | awk '{print $1}')"
	fi
	if build_info="$(go version -m "${apidiff_path}" 2>/dev/null)"; then
		apidiff_go_version_m="${build_info//$'\n'/; }"
	else
		apidiff_go_version_m="unavailable"
	fi
	apidiff_command=("${apidiff_path}")
	apidiff_source="controlled-path"
elif [[ "${network_flag}" == "1" ]]; then
	apidiff_command=(go run "${apidiff}")
	apidiff_source="pinned-go-run"
else
	printf 'APIDIFF_BIN is required for offline API compatibility checks; set API_COMPAT_ALLOW_NETWORK=1 for the pinned fallback\n' >&2
	exit 1
fi

record_mode() {
	local line
	for line in \
		"api_compat_network=${network_mode}" \
		"api_compat_network_opt_in=${network_flag}" \
		"api_baseline_rebuild=${rebuild_mode}" \
		"api_baseline_rebuild_opt_in=${rebuild_flag}" \
		"apidiff_source=${apidiff_source}" \
		"apidiff_path=${apidiff_path}" \
		"apidiff_sha256=${apidiff_sha256}" \
		"apidiff_go_version_m=${apidiff_go_version_m}"; do
		printf '%s\n' "${line}"
		if [[ -n "${API_COMPAT_METADATA:-}" ]]; then
			printf '%s\n' "${line}" >>"${API_COMPAT_METADATA}"
		fi
	done
}
record_mode

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
		GOWORK=off "${apidiff_command[@]}" -m -w "${rebuilt}" "${module}"
	)
	if ! cmp -s "${baseline}" "${rebuilt}"; then
		printf 'public %s@%s rebuild does not match committed API baseline\n' "${module}" "${baseline_version}" >&2
		rm -rf "${temporary}"
		return 1
	fi
	rm -rf "${temporary}"
	printf 'v0.9.1 API baseline reproducibly rebuilt from the public module cache\n'
}

case "${rebuild_flag}" in
0) ;;
1) rebuild_baseline ;;
esac

if ! incompatible="$("${apidiff_command[@]}" -m -incompatible "${baseline}" "${module}")"; then
	printf 'v0.9.1 API compatibility analysis failed\n' >&2
	exit 1
fi
if [[ -n "${incompatible}" ]]; then
	printf 'v0.9.1 public API incompatibilities detected:\n%s\n' "${incompatible}" >&2
	exit 1
fi
printf 'v0.9.1 public API compatibility meets the additive-only policy\n'

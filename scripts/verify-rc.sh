#!/usr/bin/env bash
set -uo pipefail

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

resolve_tool() {
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
		resolved_source="controlled-binary"
		tool_evidence=(
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
		resolved_source="pinned-go-run"
		tool_evidence=(
			"${tool_id}_pinned_module=${pinned_module}"
		)
		return 0
	fi
	printf '%s is required in offline mode; set %s and independently trusted %s\n' "${command_name}" "${env_name}" "${expected_digest_env}" >&2
	return 1
}

sha256_file() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{print $1}'
	else
		shasum -a 256 "$1" | awk '{print $1}'
	fi
}

resolve_offline_govulncheck_db() {
	local configured="${GOVULNCHECK_DB:-}"
	local database_path
	case "${configured}" in
	file:///*) database_path="${configured#file://}" ;;
	/*) database_path="${configured}" ;;
	*)
		printf 'GOVULNCHECK_DB must be a local absolute path or file:// URI in offline mode\n' >&2
		return 1
		;;
	esac
	if [[ -L "${database_path}" || ! -d "${database_path}" ]]; then
		printf 'GOVULNCHECK_DB must name a non-symlink local directory: %s\n' "${database_path}" >&2
		return 1
	fi
	database_path="$(cd "${database_path}" && pwd -P)" || return $?

	local manifest="${GOVULNCHECK_DB_MANIFEST:-}"
	if [[ "${manifest}" != /* || -L "${manifest}" || ! -f "${manifest}" ]]; then
		printf 'GOVULNCHECK_DB_MANIFEST must name an absolute, non-symlink regular file\n' >&2
		return 1
	fi
	manifest="$(cd "$(dirname "${manifest}")" && pwd -P)/$(basename "${manifest}")"
	local expected_manifest_digest="${GOVULNCHECK_DB_MANIFEST_EXPECTED_SHA256:-}"
	if [[ ! "${expected_manifest_digest}" =~ ^[[:xdigit:]]{64}$ ]]; then
		printf 'GOVULNCHECK_DB_MANIFEST_EXPECTED_SHA256 must be a 64-character SHA-256 digest\n' >&2
		return 1
	fi
	expected_manifest_digest="$(printf '%s' "${expected_manifest_digest}" | tr '[:upper:]' '[:lower:]')"
	local actual_manifest_digest
	actual_manifest_digest="$(sha256_file "${manifest}")" || return $?
	if [[ "${actual_manifest_digest}" != "${expected_manifest_digest}" ]]; then
		printf 'GOVULNCHECK_DB_MANIFEST_EXPECTED_SHA256 mismatch: expected %s, got %s\n' "${expected_manifest_digest}" "${actual_manifest_digest}" >&2
		return 1
	fi

	local checksum path extra entries=0 manifest_paths=""
	while read -r checksum path extra; do
		[[ -z "${checksum}" ]] && continue
		path="${path#\*}"
		path="${path#./}"
		if [[ ! "${checksum}" =~ ^[[:xdigit:]]{64}$ || -z "${path}" || -n "${extra:-}" ]]; then
			printf 'GOVULNCHECK_DB_MANIFEST contains an invalid entry\n' >&2
			return 1
		fi
		case "${path}" in
		/* | .. | ../* | */../*)
			printf 'GOVULNCHECK_DB_MANIFEST entries must stay inside the database: %s\n' "${path}" >&2
			return 1
			;;
		esac
		manifest_paths+="${path}"$'\n'
		entries=$((entries + 1))
	done <"${manifest}"
	if [[ "${entries}" -eq 0 ]]; then
		printf 'GOVULNCHECK_DB_MANIFEST must contain at least one database file\n' >&2
		return 1
	fi
	local duplicate_paths
	duplicate_paths="$(printf '%s' "${manifest_paths}" | LC_ALL=C sort | uniq -d)"
	if [[ -n "${duplicate_paths}" ]]; then
		printf 'GOVULNCHECK_DB_MANIFEST contains a duplicate path: %s\n' "${duplicate_paths}" >&2
		return 1
	fi
	local symbolic_links
	symbolic_links="$(find "${database_path}" -type l -print)" || return $?
	if [[ -n "${symbolic_links}" ]]; then
		printf 'GOVULNCHECK_DB must not contain symbolic links: %s\n' "${symbolic_links}" >&2
		return 1
	fi
	local manifest_file_set database_file_set
	manifest_file_set="$(printf '%s' "${manifest_paths}" | LC_ALL=C sort)"
	database_file_set="$(cd "${database_path}" && find . -type f -print | sed 's#^\./##' | LC_ALL=C sort)" || return $?
	if [[ "${manifest_file_set}" != "${database_file_set}" ]]; then
		printf 'GOVULNCHECK_DB_MANIFEST must cover exactly every regular file in GOVULNCHECK_DB\n' >&2
		return 1
	fi
	if command -v sha256sum >/dev/null 2>&1; then
		(cd "${database_path}" && sha256sum -c "${manifest}" >/dev/null) || return $?
	else
		(cd "${database_path}" && shasum -a 256 -c "${manifest}" >/dev/null) || return $?
	fi

	govulncheck_scan_args=(-db "file://${database_path}")
	govulncheck_db_evidence=(
		"govulncheck_db_source=local-snapshot"
		"govulncheck_db_path=${database_path}"
		"govulncheck_db_manifest_path=${manifest}"
		"govulncheck_db_manifest_expected_sha256=${expected_manifest_digest}"
		"govulncheck_db_manifest_actual_sha256=${actual_manifest_digest}"
	)
}

export RC_ALLOW_NETWORK="${network_flag}"

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${repository_root}"

starting_status="$(git status --porcelain=v1 --untracked-files=all)" || exit $?
if [[ -n "${starting_status}" ]]; then
	printf 'RC verification requires a completely clean repository before any tests:\n%s\n' "${starting_status}" >&2
	exit 1
fi

resolved_command=()
resolved_source=""
tool_evidence=()
resolve_tool staticcheck STATICCHECK_BIN STATICCHECK_EXPECTED_SHA256 staticcheck honnef.co/go/tools/cmd/staticcheck@v0.7.0 honnef.co/go/tools/cmd/staticcheck honnef.co/go/tools v0.7.0 || exit $?
staticcheck_command=("${resolved_command[@]}")
staticcheck_source="${resolved_source}"
staticcheck_evidence=("${tool_evidence[@]}")
resolve_tool govulncheck GOVULNCHECK_BIN GOVULNCHECK_EXPECTED_SHA256 govulncheck golang.org/x/vuln/cmd/govulncheck@v1.6.0 golang.org/x/vuln/cmd/govulncheck golang.org/x/vuln v1.6.0 || exit $?
govulncheck_command=("${resolved_command[@]}")
govulncheck_source="${resolved_source}"
govulncheck_evidence=("${tool_evidence[@]}")

govulncheck_scan_args=()
govulncheck_db_evidence=()
if [[ "${network_flag}" == "0" ]]; then
	resolve_offline_govulncheck_db || exit $?
fi

commit="$(git rev-parse HEAD)" || exit $?
tree="$(git rev-parse 'HEAD^{tree}')" || exit $?
base_ref="${RC_BASE_REF:-main}"
coverage_minimum="70.0"
critical_coverage_minimum="80.0"
remote_hygiene="${RC_REMOTE_HYGIENE-0}"
case "${remote_hygiene}" in
0 | 1) ;;
*)
	printf 'RC_REMOTE_HYGIENE must be 0 or 1\n' >&2
	exit 1
	;;
esac
if ! base_ref_commit="$(git rev-parse --verify --quiet "${base_ref}^{commit}")"; then
	printf 'RC base ref does not exist: %s\n' "${base_ref}" >&2
	exit 1
fi
if ! git merge-base --is-ancestor "${base_ref_commit}" HEAD; then
	printf 'RC base ref must be an ancestor of HEAD: %s (%s)\n' "${base_ref}" "${base_ref_commit}" >&2
	exit 1
fi
if ! base_commit="$(git merge-base "${base_ref_commit}" HEAD)"; then
	printf 'RC base ref has no merge base with HEAD: %s\n' "${base_ref}" >&2
	exit 1
fi

shuffle_seed="${SHUFFLE_SEED:-$(printf '%s\n%s\n' "${commit}" "${tree}" | cksum | awk '{print $1}')}"
artifact_dir="${RC_ARTIFACT_DIR:-$(mktemp -d "${TMPDIR:-/tmp}/gin-bear-rc.XXXXXX")}"
mkdir -p "${artifact_dir}"
artifact_dir="$(cd "${artifact_dir}" && pwd)"
summary="${artifact_dir}/results.tsv"
metadata="${artifact_dir}/metadata.txt"
initial_status="${artifact_dir}/git-status.before"
final_status="${artifact_dir}/git-status.after"
release_tag="${RC_RELEASE_TAG:-}"
release_tag_type="not-applicable"
release_tag_target="not-applicable"
tag_signature_verification="not-applicable"
signature_policy="not-applicable"
if [[ "${RC_VERIFY_TAG_SIGNATURE+x}" == "x" ]]; then
	case "${RC_VERIFY_TAG_SIGNATURE}" in
	true | false) signature_policy="${RC_VERIFY_TAG_SIGNATURE}" ;;
	*)
		printf 'RC_VERIFY_TAG_SIGNATURE must be true or false\n' >&2
		exit 1
		;;
	esac
	if [[ -z "${release_tag}" ]]; then
		printf 'RC_VERIFY_TAG_SIGNATURE requires RC_RELEASE_TAG\n' >&2
		exit 1
	fi
fi

if [[ -n "${release_tag}" ]]; then
	if [[ "${RC_VERIFY_TAG_SIGNATURE+x}" != "x" ]]; then
		printf 'RC_VERIFY_TAG_SIGNATURE must be explicitly set to true or false when RC_RELEASE_TAG is set\n' >&2
		exit 1
	fi
	expected_version="${RC_EXPECTED_VERSION:-}"
	if [[ -z "${expected_version}" ]]; then
		printf 'RC_EXPECTED_VERSION is required when RC_RELEASE_TAG is set\n' >&2
		exit 1
	fi
	if [[ "${release_tag}" != "${expected_version}" ]]; then
		printf 'release tag %s does not match release version %s\n' "${release_tag}" "${expected_version}" >&2
		exit 1
	fi
	if ! release_tag_type="$(git cat-file -t "refs/tags/${release_tag}" 2>/dev/null)"; then
		printf 'release tag does not exist: %s\n' "${release_tag}" >&2
		exit 1
	fi
	if [[ "${release_tag_type}" != "tag" ]]; then
		printf 'release tag must be annotated: %s has object type %s\n' "${release_tag}" "${release_tag_type}" >&2
		exit 1
	fi
	release_tag_target="$(git rev-parse "${release_tag}^{commit}")" || exit $?
	if [[ "${release_tag_target}" != "${commit}" ]]; then
		printf 'release tag must target HEAD: %s targets %s, HEAD is %s\n' "${release_tag}" "${release_tag_target}" "${commit}" >&2
		exit 1
	fi
	case "${signature_policy}" in
	true)
		trusted_keyring="${RC_TRUSTED_KEYRING:-}"
		if [[ -z "${trusted_keyring}" || ! -d "${trusted_keyring}" ]]; then
			printf 'RC_TRUSTED_KEYRING must name a trusted GNUPGHOME directory when signature verification is true\n' >&2
			exit 1
		fi
		if ! GNUPGHOME="${trusted_keyring}" git verify-tag --raw "${release_tag}" >"${artifact_dir}/tag-signature.log" 2>&1; then
			cat "${artifact_dir}/tag-signature.log" >&2
			printf 'release tag signature verification failed: %s\n' "${release_tag}" >&2
			exit 1
		fi
		if ! grep -q '\[GNUPG:\] VALIDSIG ' "${artifact_dir}/tag-signature.log" ||
			! grep -Eq '\[GNUPG:\] TRUST_(FULLY|ULTIMATE) ' "${artifact_dir}/tag-signature.log"; then
			cat "${artifact_dir}/tag-signature.log" >&2
			printf 'release tag signature is valid but not trusted by RC_TRUSTED_KEYRING: %s\n' "${release_tag}" >&2
			exit 1
		fi
		tag_signature_verification="verified-with-trusted-keyring"
		;;
	false)
		tag_signature_verification="explicitly-exempted"
		printf 'Release tag signature verification explicitly exempted by RC_VERIFY_TAG_SIGNATURE=false\n'
		;;
	esac
fi

printf '%s' "${starting_status}" >"${initial_status}"
: >"${summary}"

capture_version() {
	local label="$1"
	shift
	local output
	if ! output="$("$@" 2>&1)"; then
		printf '%s version command failed: %s\n' "${label}" "${output}" >&2
		return 1
	fi
	printf '%s=%s\n' "${label}" "${output//$'\n'/; }" >>"${metadata}"
}

{
	printf 'commit=%s\n' "${commit}"
	printf 'tree=%s\n' "${tree}"
	printf 'base_ref=%s\n' "${base_ref}"
	printf 'base_ref_commit=%s\n' "${base_ref_commit}"
	printf 'base_commit=%s\n' "${base_commit}"
	printf 'release_tag=%s\n' "${release_tag:-not-applicable}"
	printf 'release_tag_type=%s\n' "${release_tag_type}"
	printf 'release_tag_target=%s\n' "${release_tag_target}"
	printf 'signature_policy=%s\n' "${signature_policy}"
	printf 'tag_signature_verification=%s\n' "${tag_signature_verification}"
	printf 'coverage_minimum=%s\n' "${coverage_minimum}"
	printf 'critical_coverage_minimum=%s\n' "${critical_coverage_minimum}"
	printf 'shuffle_seed=%s\n' "${shuffle_seed}"
	printf 'remote_hygiene=%s\n' "${remote_hygiene}"
	printf 'network_mode=%s\n' "${network_mode}"
	printf 'network_opt_in=%s\n' "${network_flag}"
	printf 'staticcheck_source=%s\n' "${staticcheck_source}"
	printf 'govulncheck_source=%s\n' "${govulncheck_source}"
	printf '%s\n' "${staticcheck_evidence[@]}"
	printf '%s\n' "${govulncheck_evidence[@]}"
	printf '%s\n' "${govulncheck_db_evidence[@]}"
	printf 'artifact_dir=%s\n' "${artifact_dir}"
	printf 'GOPROXY=%s\n' "${GOPROXY}"
	printf 'GOSUMDB=%s\n' "${GOSUMDB}"
	printf 'GOTOOLCHAIN=%s\n' "${GOTOOLCHAIN}"
} >"${metadata}"
capture_version go go version || exit $?
capture_version staticcheck "${staticcheck_command[@]}" -version || exit $?
capture_version govulncheck "${govulncheck_command[@]}" -version || exit $?

printf 'RC commit: %s\n' "${commit}"
printf 'RC tree: %s\n' "${tree}"
printf 'RC base: %s (%s)\n' "${base_ref}" "${base_commit}"
printf 'Shuffle seed: %s\n' "${shuffle_seed}"
printf 'Audit logs: %s\n' "${artifact_dir}"
cat "${metadata}"

finish() {
	local exit_code=$?
	trap - EXIT
	if ! git status --porcelain=v1 --untracked-files=all >"${final_status}"; then
		printf 'could not inspect final repository status\n' >&2
		exit_code=1
	elif [[ -s "${final_status}" ]]; then
		printf 'repository is not clean after RC verification; see %s\n' "${final_status}" >&2
		exit_code=1
	fi
	if ! diff -u "${initial_status}" "${final_status}" >"${artifact_dir}/git-status.diff"; then
		printf 'repository status changed during RC verification; see %s\n' "${artifact_dir}/git-status.diff" >&2
		exit_code=1
	else
		rm -f "${artifact_dir}/git-status.diff"
	fi
	printf 'final\texit_code\t%d\n' "${exit_code}" | tee -a "${summary}"
	exit "${exit_code}"
}
trap finish EXIT

run_step() {
	local name="$1"
	shift
	local log="${artifact_dir}/${name}.log"
	printf '\n==> [%s] %s\n' "${name}" "$*"
	"$@" >"${log}" 2>&1
	local exit_code=$?
	cat "${log}"
	printf '%s\texit_code\t%d\tlog=%s\n' "${name}" "${exit_code}" "${log}" | tee -a "${summary}"
	return "${exit_code}"
}

check_candidate_diff() {
	git diff --check "${base_commit}..HEAD" || return $?
	git show --check HEAD
}

check_repository_hygiene() {
	local failed=0
	local local_branches
	local_branches="$(git for-each-ref --format='%(refname:short)' refs/heads)" || return 1
	printf '%s\n' 'Local branches:'
	printf '%s\n' "${local_branches}"
	while IFS= read -r branch; do
		[[ -z "${branch}" ]] && continue
		case "${branch}" in
		main | codex/production-baseline | codex/production-framework-v010) ;;
		*)
			printf 'unexpected local branch: %s\n' "${branch}" >&2
			failed=1
			;;
		esac
	done <<<"${local_branches}"

	if [[ "${remote_hygiene}" == "1" ]]; then
		printf '%s\n' 'Remote heads:'
		local remote_heads
		remote_heads="$(git ls-remote --heads origin | awk '{print $2}')" || return 1
		printf '%s\n' "${remote_heads}"
		while IFS= read -r head; do
			[[ -z "${head}" ]] && continue
			case "${head}" in
			refs/heads/main | refs/heads/codex/production-baseline) ;;
			*)
				printf 'unexpected remote head: %s\n' "${head}" >&2
				failed=1
				;;
			esac
		done <<<"${remote_heads}"
	else
		printf '%s\n' 'Remote head audit: skipped (set RC_REMOTE_HYGIENE=1 to enable)'
	fi

	local forbidden
	forbidden="$(find . -path './.git' -prune -o -type f \( -name Dockerfile -o -name 'Dockerfile.*' -o -name docker-compose.yml -o -name docker-compose.yaml -o -name compose.yml -o -name compose.yaml -o -path '*/kubernetes/*' -o -path '*/helm/*' -o -name coverage.out \) -print)"
	if [[ -n "${forbidden}" ]]; then
		printf 'forbidden repository artifacts:\n%s\n' "${forbidden}" >&2
		failed=1
	fi
	return "${failed}"
}

run_step clean go clean -testcache || exit $?
run_step count1 go test ./... -count=1 || exit $?
run_step shuffle20 go test ./... -shuffle="${shuffle_seed}" -count=20 -timeout=30m || exit $?
run_step race3 go test -race ./... -count=3 || exit $?
run_step vet go vet ./... || exit $?
run_step staticcheck "${staticcheck_command[@]}" ./... || exit $?
run_step govulncheck "${govulncheck_command[@]}" "${govulncheck_scan_args[@]}" ./... || exit $?
run_step release-check env COVERAGE_MINIMUM="${coverage_minimum}" CRITICAL_COVERAGE_MINIMUM="${critical_coverage_minimum}" API_COMPAT_METADATA="${metadata}" RELEASE_CHECK_METADATA="${metadata}" scripts/release-check.sh || exit $?
run_step diff-check check_candidate_diff || exit $?
run_step hygiene check_repository_hygiene || exit $?

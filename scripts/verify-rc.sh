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
		resolved_source="controlled-path"
		return 0
	fi
	local installed
	if installed="$(command -v "${command_name}")"; then
		resolved_command=("${installed}")
		resolved_source="local-binary"
		return 0
	fi
	if [[ "${network_flag}" == "1" ]]; then
		resolved_command=(go run "${pinned_module}")
		resolved_source="pinned-go-run"
		return 0
	fi
	printf '%s is required in offline mode; preinstall it or set %s to an executable path\n' "${command_name}" "${env_name}" >&2
	return 1
}

resolved_command=()
resolved_source=""
resolve_tool STATICCHECK_BIN staticcheck honnef.co/go/tools/cmd/staticcheck@v0.7.0 || exit $?
staticcheck_command=("${resolved_command[@]}")
staticcheck_source="${resolved_source}"
resolve_tool GOVULNCHECK_BIN govulncheck golang.org/x/vuln/cmd/govulncheck@v1.6.0 || exit $?
govulncheck_command=("${resolved_command[@]}")
govulncheck_source="${resolved_source}"

govulncheck_scan_args=()
if [[ "${network_flag}" == "0" ]]; then
	if [[ -z "${GOVULNCHECK_DB:-}" ]]; then
		printf 'GOVULNCHECK_DB is required in offline mode and must point to a local vulnerability database\n' >&2
		exit 1
	fi
	govulncheck_scan_args=(-db "${GOVULNCHECK_DB}")
fi
export RC_ALLOW_NETWORK="${network_flag}"

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${repository_root}"

starting_status="$(git status --porcelain=v1 --untracked-files=all)" || exit $?
if [[ -n "${starting_status}" ]]; then
	printf 'RC verification requires a completely clean repository before any tests:\n%s\n' "${starting_status}" >&2
	exit 1
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

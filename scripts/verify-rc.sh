#!/usr/bin/env bash
set -uo pipefail

export GOSUMDB=sum.golang.org
export GOTOOLCHAIN=go1.25.12

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
if ! base_ref_commit="$(git rev-parse --verify --quiet "${base_ref}^{commit}")"; then
	printf 'RC base ref does not exist: %s\n' "${base_ref}" >&2
	exit 1
fi
if ! base_commit="$(git merge-base "${base_ref_commit}" HEAD)"; then
	printf 'RC base ref has no merge base with HEAD: %s\n' "${base_ref}" >&2
	exit 1
fi

shuffle_seed="${SHUFFLE_SEED:-$(date +%s)}"
artifact_dir="${RC_ARTIFACT_DIR:-$(mktemp -d "${TMPDIR:-/tmp}/gin-bear-rc.XXXXXX")}"
mkdir -p "${artifact_dir}"
artifact_dir="$(cd "${artifact_dir}" && pwd)"
summary="${artifact_dir}/results.tsv"
metadata="${artifact_dir}/metadata.txt"
initial_status="${artifact_dir}/git-status.before"
final_status="${artifact_dir}/git-status.after"

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
	printf 'shuffle_seed=%s\n' "${shuffle_seed}"
	printf 'artifact_dir=%s\n' "${artifact_dir}"
	printf 'GOSUMDB=%s\n' "${GOSUMDB}"
	printf 'GOTOOLCHAIN=%s\n' "${GOTOOLCHAIN}"
} >"${metadata}"
capture_version go go version || exit $?
capture_version staticcheck go run honnef.co/go/tools/cmd/staticcheck@v0.7.0 -version || exit $?
capture_version govulncheck go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 -version || exit $?

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
run_step shuffle20 go test ./... -shuffle="${shuffle_seed}" -count=20 || exit $?
run_step race3 go test -race ./... -count=3 || exit $?
run_step vet go vet ./... || exit $?
run_step staticcheck go run honnef.co/go/tools/cmd/staticcheck@v0.7.0 ./... || exit $?
run_step govulncheck go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./... || exit $?
run_step release-check scripts/release-check.sh || exit $?
run_step diff-check check_candidate_diff || exit $?
run_step hygiene check_repository_hygiene || exit $?

#!/usr/bin/env bash
set -uo pipefail

export GOSUMDB=sum.golang.org
export GOTOOLCHAIN=go1.25.12

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${repository_root}"

commit="$(git rev-parse HEAD)"
shuffle_seed="${SHUFFLE_SEED:-$(date +%s)}"
artifact_dir="${RC_ARTIFACT_DIR:-$(mktemp -d "${TMPDIR:-/tmp}/gin-bear-rc.XXXXXX")}"
mkdir -p "${artifact_dir}"
artifact_dir="$(cd "${artifact_dir}" && pwd)"
summary="${artifact_dir}/results.tsv"
metadata="${artifact_dir}/metadata.txt"
initial_status="${artifact_dir}/git-status.before"
final_status="${artifact_dir}/git-status.after"

git status --porcelain=v1 --untracked-files=all >"${initial_status}"
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
	printf 'shuffle_seed=%s\n' "${shuffle_seed}"
	printf 'artifact_dir=%s\n' "${artifact_dir}"
	printf 'GOSUMDB=%s\n' "${GOSUMDB}"
	printf 'GOTOOLCHAIN=%s\n' "${GOTOOLCHAIN}"
} >"${metadata}"
capture_version go go version || exit $?
capture_version staticcheck go run honnef.co/go/tools/cmd/staticcheck@v0.7.0 -version || exit $?
capture_version govulncheck go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 -version || exit $?

printf 'RC commit: %s\n' "${commit}"
printf 'Shuffle seed: %s\n' "${shuffle_seed}"
printf 'Audit logs: %s\n' "${artifact_dir}"
cat "${metadata}"

finish() {
	local exit_code=$?
	trap - EXIT
	git status --porcelain=v1 --untracked-files=all >"${final_status}"
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

check_repository_hygiene() {
	local failed=0
	printf '%s\n' 'Local branches:'
	git branch --format='%(refname:short)'
	printf '%s\n' 'Remote heads:'
	local remote_heads
	remote_heads="$(git ls-remote --heads origin | awk '{print $2}')" || return 1
	printf '%s\n' "${remote_heads}"
	while IFS= read -r head; do
		[[ -z "${head}" ]] && continue
		case "${head}" in
		refs/heads/main|refs/heads/codex/production-baseline) ;;
		*)
			printf 'unexpected remote head: %s\n' "${head}" >&2
			failed=1
			;;
		esac
	done <<<"${remote_heads}"

	local forbidden
	forbidden="$(find . -maxdepth 4 -type f \( -name Dockerfile -o -name docker-compose.yml -o -name docker-compose.yaml -o -path '*/kubernetes/*' -o -path '*/helm/*' -o -name coverage.out \) -print)"
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
run_step diff-check git diff --check || exit $?
run_step hygiene check_repository_hygiene || exit $?

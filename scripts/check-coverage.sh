#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repository_root="${CRITICAL_COVERAGE_REPOSITORY_ROOT:-$(cd "${script_dir}/.." && pwd)}"
manifest="${CRITICAL_COVERAGE_MANIFEST:-${script_dir}/critical-coverage-files.txt}"
profile="${1:-coverage.out}"
minimum="${COVERAGE_MINIMUM:-70.0}"
critical_minimum="${CRITICAL_COVERAGE_MINIMUM:-80.0}"

if [[ ! -f "${profile}" ]]; then
	printf 'coverage profile not found: %s\n' "${profile}" >&2
	exit 1
fi
if [[ ! -f "${manifest}" ]]; then
	printf 'critical coverage manifest not found: %s\n' "${manifest}" >&2
	exit 1
fi

validate_threshold() {
	local name="$1"
	local value="$2"
	if [[ ! "${value}" =~ ^[0-9]+([.][0-9]+)?$ ]] || ! awk -v value="${value}" 'BEGIN { exit !(value > 0 && value <= 100) }'; then
		printf '%s must be a finite decimal greater than 0 and at most 100: %s\n' "${name}" "${value}" >&2
		return 1
	fi
}

validate_profile() {
	awk '
		NR == 1 {
			if ($0 !~ /^mode: (set|count|atomic)$/) {
				printf "malformed coverage profile header at line 1: %s\n", $0 > "/dev/stderr"
				failed = 1
			}
			next
		}
		{
			if ($0 !~ /^.+:[1-9][0-9]*\.[1-9][0-9]*,[1-9][0-9]*\.[1-9][0-9]* [1-9][0-9]* [0-9]+$/) {
				printf "malformed coverage profile data at line %d: %s\n", NR, $0 > "/dev/stderr"
				failed = 1
				next
			}
			split($0, fields, " ")
			total += fields[2]
		}
		END {
			if (NR == 0) {
				printf "malformed coverage profile: empty file\n" > "/dev/stderr"
				failed = 1
			} else if (total <= 0) {
				printf "malformed coverage profile: statement total must be greater than 0\n" > "/dev/stderr"
				failed = 1
			}
			exit failed
		}
	' "${profile}"
}

check_threshold() {
	local prefix="$1"
	local covered="$2"
	local total="$3"
	local threshold="$4"
	awk -v prefix="${prefix}" -v covered="${covered}" -v total="${total}" -v minimum="${threshold}" 'BEGIN {
		actual = 100 * covered / total
		if (100 * covered < minimum * total) {
			printf "%s %.1f%% is below %.1f%%\n", prefix, actual, minimum
			exit 1
		}
		printf "%s %.1f%% meets %.1f%%\n", prefix, actual, minimum
	}'
}

derive_bear_manifest_entries() {
	local filename="$1"
	case "${filename}" in
	bear.go)
		printf 'handler pkg/bear/%s\n' "${filename}"
		printf 'lifecycle pkg/bear/%s\n' "${filename}"
		;;
	fairing*.go | handler*.go | responder*.go) printf 'handler pkg/bear/%s\n' "${filename}" ;;
	lifecycle*.go) printf 'lifecycle pkg/bear/%s\n' "${filename}" ;;
	auth*.go | jwt*.go) printf 'auth pkg/bear/%s\n' "${filename}" ;;
	binding*.go) printf 'binding pkg/bear/%s\n' "${filename}" ;;
	error*.go | http_error*.go) printf 'errors pkg/bear/%s\n' "${filename}" ;;
	config_loader*.go) printf 'config-loader pkg/bear/%s\n' "${filename}" ;;
	migration*.go) printf 'migration-lock pkg/bear/%s\n' "${filename}" ;;
	cron_lock*.go) printf 'cron-lock pkg/bear/%s\n' "${filename}" ;;
	esac
}

list_package_files() {
	local package_path="$1"
	(cd "${repository_root}" && go list -f '{{range .GoFiles}}{{println .}}{{end}}' "${package_path}")
}

derive_manifest_entries() {
	local filename bear_files cli_files scaffold_files
	bear_files="$(list_package_files ./pkg/bear)" || return 1
	cli_files="$(list_package_files ./internal/cli)" || return 1
	scaffold_files="$(list_package_files ./internal/scaffold)" || return 1
	while IFS= read -r filename; do
		[[ -z "${filename}" ]] || derive_bear_manifest_entries "${filename}"
	done <<<"${bear_files}"
	while IFS= read -r filename; do
		[[ -z "${filename}" ]] || printf 'cli internal/cli/%s\n' "${filename}"
	done <<<"${cli_files}"
	while IFS= read -r filename; do
		[[ -z "${filename}" ]] || printf 'scaffold internal/scaffold/%s\n' "${filename}"
	done <<<"${scaffold_files}"
}

audit_critical_manifest() {
	local expected
	expected="$(derive_manifest_entries)" || return 1
	awk '
		FNR == NR {
			if (NF != 2) {
				printf "invalid derived critical coverage ownership: %s\n", $0 > "/dev/stderr"
				failed = 1
				next
			}
			expected[$1 SUBSEP $2] = 1
			next
		}
		/^[[:space:]]*($|#)/ { next }
		{
			if (NF != 2) {
				printf "invalid critical coverage manifest line %d: %s\n", FNR, $0 > "/dev/stderr"
				failed = 1
				next
			}
			key = $1 SUBSEP $2
			if (!(key in expected)) {
				printf "critical coverage manifest has extra or incorrect ownership %s %s\n", $1, $2 > "/dev/stderr"
				failed = 1
			}
			if (seen[key]++) {
				printf "critical coverage manifest duplicates ownership %s %s\n", $1, $2 > "/dev/stderr"
				failed = 1
			}
		}
		END {
			for (key in expected) {
				if (!seen[key]) {
					split(key, fields, SUBSEP)
					printf "critical coverage %s manifest missing current platform production file %s\n", fields[1], fields[2] > "/dev/stderr"
					failed = 1
				}
			}
			exit failed
		}
	' <(printf '%s\n' "${expected}") "${manifest}"
}

validate_threshold COVERAGE_MINIMUM "${minimum}"
validate_threshold CRITICAL_COVERAGE_MINIMUM "${critical_minimum}"
validate_profile
audit_critical_manifest

overall="$(awk '
	NR == 1 { next }
	{ total += $2; if ($3 > 0) covered += $2 }
	END { printf "%d %d", covered, total }
' "${profile}")"
read -r overall_covered overall_total <<<"${overall}"
check_threshold "coverage" "${overall_covered}" "${overall_total}" "${minimum}"

while IFS= read -r label; do
	[[ -z "${label}" ]] && continue
	result="$(awk -v label="${label}" '
		FNR == NR {
			if ($0 !~ /^[[:space:]]*#/ && $1 == label) wanted[$2] = 1
			next
		}
		FNR == 1 { next }
		{
			source = $1
			sub(/:[0-9].*$/, "", source)
			for (path in wanted) {
				if (source == path || (length(source) > length(path) && substr(source, length(source) - length(path), 1) == "/" && substr(source, length(source) - length(path) + 1) == path)) {
					seen[path] = 1
					total += $2
					if ($3 > 0) covered += $2
					break
				}
			}
		}
		END {
			failed = 0
			for (path in wanted) {
				if (!seen[path]) {
					printf "critical coverage %s missing production file %s from profile\n", label, path > "/dev/stderr"
					failed = 1
				}
			}
			if (failed || total == 0) exit 2
			printf "%d %d", covered, total
		}
	' "${manifest}" "${profile}")" || exit 1
	read -r covered total <<<"${result}"
	check_threshold "critical coverage ${label}" "${covered}" "${total}" "${critical_minimum}"
done < <(awk '$0 !~ /^[[:space:]]*#/ && NF == 2 && !seen[$1]++ { print $1 }' "${manifest}")

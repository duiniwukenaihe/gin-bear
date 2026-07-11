#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repository_root="$(cd "${script_dir}/.." && pwd)"
manifest="${script_dir}/critical-coverage-files.txt"
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

overall="$(awk '
	NR == 1 { next }
	{ total += $2; if ($3 > 0) covered += $2 }
	END {
		if (total == 0) exit 2
		printf "%d %d", covered, total
	}
' "${profile}")" || {
	printf 'coverage profile has no statements: %s\n' "${profile}" >&2
	exit 1
}
read -r overall_covered overall_total <<<"${overall}"
check_threshold "coverage" "${overall_covered}" "${overall_total}" "${minimum}"

audit_scaffold_manifest() {
	local go_files
	go_files="$(cd "${repository_root}" && go list -f '{{range .GoFiles}}{{println .}}{{end}}' ./internal/scaffold)"
	while IFS= read -r filename; do
		[[ -z "${filename}" ]] && continue
		if ! awk -v path="internal/scaffold/${filename}" '$1 == "scaffold" && $2 == path { found = 1 } END { exit !found }' "${manifest}"; then
			printf 'critical coverage scaffold manifest missing current platform production file internal/scaffold/%s\n' "${filename}" >&2
			return 1
		fi
	done <<<"${go_files}"
	while read -r label path; do
		[[ "${label}" != "scaffold" ]] && continue
		local filename="${path##*/}"
		if ! grep -Fxq "${filename}" <<<"${go_files}"; then
			printf 'critical coverage scaffold manifest lists non-production file %s for current platform\n' "${path}" >&2
			return 1
		fi
	done <"${manifest}"
}

if awk -v minimum="${critical_minimum}" 'BEGIN { exit !(minimum + 0 > 0) }'; then
	audit_scaffold_manifest
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
fi

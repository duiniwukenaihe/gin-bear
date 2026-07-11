#!/usr/bin/env bash
set -euo pipefail

profile="${1:-coverage.out}"
minimum="${COVERAGE_MINIMUM:-70.0}"
critical_minimum="${CRITICAL_COVERAGE_MINIMUM:-80.0}"

if [[ ! -f "${profile}" ]]; then
	printf 'coverage profile not found: %s\n' "${profile}" >&2
	exit 1
fi

actual="$(go tool cover -func="${profile}" | awk '/^total:/ {gsub("%", "", $3); print $3}')"
awk -v actual="${actual}" -v minimum="${minimum}" 'BEGIN {
  if (actual + 0 < minimum + 0) {
    printf "coverage %.1f%% is below %.1f%%\n", actual, minimum
    exit 1
  }
  printf "coverage %.1f%% meets %.1f%%\n", actual, minimum
}'

if awk -v minimum="${critical_minimum}" 'BEGIN { exit !(minimum + 0 > 0) }'; then
	check_critical_coverage() {
		local label="$1"
		local pattern="$2"
		local result
		result="$(awk -v pattern="${pattern}" '
			NR == 1 { next }
			{
				source = $1
				sub(/:[0-9].*$/, "", source)
				if (source ~ pattern) {
					total += $2
					if ($3 > 0) covered += $2
				}
			}
			END {
				if (total == 0) exit 2
				printf "%.1f", 100 * covered / total
			}
		' "${profile}")" || {
			printf 'critical coverage %s has no statements in %s\n' "${label}" "${profile}" >&2
			return 1
		}
		awk -v label="${label}" -v actual="${result}" -v minimum="${critical_minimum}" 'BEGIN {
			if (actual + 0 < minimum + 0) {
				printf "critical coverage %s %.1f%% is below %.1f%%\n", label, actual, minimum
				exit 1
			}
			printf "critical coverage %s %.1f%% meets %.1f%%\n", label, actual, minimum
		}'
	}

	check_critical_coverage handler '(^|/)pkg/bear/handler[.]go$'
	check_critical_coverage binding '(^|/)pkg/bear/binding[.]go$'
	check_critical_coverage errors '(^|/)pkg/bear/(error|http_error)[.]go$'
	check_critical_coverage config-loader '(^|/)pkg/bear/config_loader[.]go$'
	check_critical_coverage lifecycle '(^|/)pkg/bear/lifecycle[.]go$'
	check_critical_coverage auth '(^|/)pkg/bear/(auth_token|jwt|jwt_fairing)[.]go$'
	check_critical_coverage migration-lock '(^|/)pkg/bear/migration[.]go$'
	check_critical_coverage cron-lock '(^|/)pkg/bear/cron_lock[.]go$'
	check_critical_coverage cli '(^|/)internal/cli/[^/]+[.]go$'
	check_critical_coverage scaffold '(^|/)internal/scaffold/embed[.]go$'
fi

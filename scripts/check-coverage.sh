#!/usr/bin/env bash
set -euo pipefail

profile="${1:-coverage.out}"
minimum="${COVERAGE_MINIMUM:-60.0}"
actual="$(go tool cover -func="${profile}" | awk '/^total:/ {gsub("%", "", $3); print $3}')"
awk -v actual="${actual}" -v minimum="${minimum}" 'BEGIN {
  if (actual + 0 < minimum + 0) {
    printf "coverage %.1f%% is below %.1f%%\n", actual, minimum
    exit 1
  }
  printf "coverage %.1f%% meets %.1f%%\n", actual, minimum
}'

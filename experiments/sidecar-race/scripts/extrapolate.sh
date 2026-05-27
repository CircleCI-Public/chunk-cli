#!/usr/bin/env bash
# Extrapolate monthly compute savings from per-iteration averages.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/common.sh
source "${SCRIPT_DIR}/lib/common.sh"

MONTHLY_BUILDS=5000
SIDECAR_AVG_SEC=30
CI_AVG_SEC=300

usage() {
  cat <<EOF
Usage: extrapolate.sh [--monthly-builds N] [--sidecar-avg-sec S] [--ci-avg-sec S]

Estimates hours reclaimed per month from measured or assumed average iteration times.

Example (using measured medians from summarize-run.sh):
  ./scripts/extrapolate.sh --monthly-builds 5000 --sidecar-avg-sec 28 --ci-avg-sec 272
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --monthly-builds) MONTHLY_BUILDS="$2"; shift 2 ;;
    --sidecar-avg-sec) SIDECAR_AVG_SEC="$2"; shift 2 ;;
    --ci-avg-sec) CI_AVG_SEC="$2"; shift 2 ;;
    -h | --help) usage; exit 0 ;;
    *) die "unknown argument: $1" ;;
  esac
done

python3 - <<PY
monthly = ${MONTHLY_BUILDS}
sidecar_s = ${SIDECAR_AVG_SEC}
ci_s = ${CI_AVG_SEC}
saved_per = ci_s - sidecar_s
ratio = ci_s / sidecar_s if sidecar_s else 0
hours_ci = monthly * ci_s / 3600
hours_sidecar = monthly * sidecar_s / 3600
hours_saved = monthly * saved_per / 3600

print(f"Monthly iterations:     {monthly:,}")
print(f"Avg sidecar (s):        {sidecar_s}")
print(f"Avg CI gate (s):        {ci_s}")
print(f"Saved per iteration:    {saved_per}s ({ratio:.1f}x faster)")
print(f"Monthly CI hours:       {hours_ci:,.1f}")
print(f"Monthly sidecar hours:  {hours_sidecar:,.1f}")
print(f"Hours reclaimed:        {hours_saved:,.1f}")
PY

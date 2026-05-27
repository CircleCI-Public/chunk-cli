#!/usr/bin/env bash
# Open draft PRs for sidecar and CI arms before starting experiment runs.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/common.sh
source "${SCRIPT_DIR}/lib/common.sh"

RUN_LABEL="${1:-001}"

require_cmd gh

echo "Bootstrapping draft PRs for run label ${RUN_LABEL}..."
"${SCRIPT_DIR}/open-run-pr.sh" --run-id "${RUN_LABEL}" --arm sidecar --bootstrap
echo ""
"${SCRIPT_DIR}/open-run-pr.sh" --run-id "${RUN_LABEL}" --arm ci --bootstrap
echo ""
echo "Done. After each run-arm.sh finishes, its PR is updated with metrics and marked ready for review."

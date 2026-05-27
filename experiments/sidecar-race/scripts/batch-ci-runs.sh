#!/usr/bin/env bash
# Run CI arm replicates 001-005 sequentially (agent + push + poll gates per task).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/common.sh
source "${SCRIPT_DIR}/lib/common.sh"

LABELS="${1:-001,002,003,004,005}"
IFS=',' read -ra RUNS <<< "${LABELS}"

LOG="${SIDECAR_RACE_BATCH_LOG:-/tmp/sidecar-race-batch-ci.log}"
LOCKDIR="${SIDECAR_RACE_BATCH_LOCK:-/tmp/sidecar-race-batch-ci.lockdir}"

export PATH="${REPO_ROOT}/dist:${PATH}"
export CIRCLECI_CREDIT_USD="${CIRCLECI_CREDIT_USD:-0.0006}"

if ! mkdir "${LOCKDIR}" 2>/dev/null; then
  echo "Another CI batch is already running (lock: ${LOCKDIR})" >&2
  exit 1
fi
trap 'rmdir "${LOCKDIR}" 2>/dev/null || true' EXIT

exec > >(tee -a "${LOG}") 2>&1

echo "=== CI arm batch started $(date -u +%Y-%m-%dT%H:%M:%SZ) pid=$$ labels=${LABELS} ==="

for RUN in "${RUNS[@]}"; do
  RUN="${RUN// /}"
  BRANCH="experiment/sidecar-race--run-${RUN}-ci"
  echo ""
  echo "========== RUN ${RUN} CI (${BRANCH}) $(date -u +%Y-%m-%dT%H:%M:%SZ) =========="

  git -C "${REPO_ROOT}" fetch origin experiment/sidecar-race
  git -C "${REPO_ROOT}" checkout experiment/sidecar-race
  git -C "${REPO_ROOT}" reset --hard origin/experiment/sidecar-race
  git -C "${REPO_ROOT}" branch -D "${BRANCH}" 2>/dev/null || true
  git -C "${REPO_ROOT}" push origin --delete "${BRANCH}" 2>/dev/null || true
  git -C "${REPO_ROOT}" checkout -b "${BRANCH}"

  "${SCRIPT_DIR}/prep-check.sh" --arm ci
  "${SCRIPT_DIR}/run-arm.sh" --arm ci --notes "run ${RUN} ci (agent)"

  RUN_ID=""
  shopt -s nullglob
  run_dirs=("${EXPERIMENT_ROOT}"/results/[0-9][0-9][0-9][0-9][0-9][0-9][0-9]-[0-9][0-9][0-9][0-9][0-9][0-9]/)
  shopt -u nullglob
  if ((${#run_dirs[@]} > 0)); then
    # shellcheck disable=SC2012
    RUN_ID="$(basename "$(ls -dt "${run_dirs[@]}" | head -1)")"
  fi
  echo "RUN ${RUN} CI complete. RUN_ID=${RUN_ID:-unknown}"

  if [[ -n "${RUN_ID}" && -d "${EXPERIMENT_ROOT}/results/${RUN_ID}" ]]; then
    git -C "${REPO_ROOT}" add -f "${EXPERIMENT_ROOT}/results/${RUN_ID}/"
    if ! git -C "${REPO_ROOT}" diff --cached --quiet; then
      git -C "${REPO_ROOT}" commit -m "experiment: results ${RUN_ID} (ci run ${RUN})"
    fi
    git -C "${REPO_ROOT}" push -u origin "${BRANCH}"
  fi
done

echo ""
echo "=== CI batch finished $(date -u +%Y-%m-%dT%H:%M:%SZ) ==="
echo "Compare: ./scripts/compare-runs.sh --from-git --labels ${LABELS}"

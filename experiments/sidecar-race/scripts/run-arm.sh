#!/usr/bin/env bash
# Run all task-bank iterations for one experiment arm (sidecar or CI).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/common.sh
source "${SCRIPT_DIR}/lib/common.sh"

ARM=""
RUN_ID=""
FROM_TASK=1
TO_TASK=10
NOTES=""
DRY_RUN=false
ENSURE_SIDECAR=true
COMMIT_CI=true
EPILOGUE=true
REPLAY_PATCHES=false

usage() {
  cat <<EOF
Usage: run-arm.sh --arm <sidecar|ci> [options]

Automates the full experiment loop: Claude Agent SDK edit → record validation timing.

Options:
  --run-id <id>       Use existing run dir (must match arm); default creates new via new-run.sh
  --from-task <n>     First task (default 1)
  --to-task <n>       Last task (default 10)
  --notes <text>      Stored in run.json
  --dry-run           Print steps without executing
  --replay-patches    Use git apply from task-bank (debug only; not a real agent run)
  --no-ensure-sidecar Skip sidecar create (sidecar arm only)
  --no-epilogue         Sidecar arm: skip final push + CI validation
  --no-commit         CI arm: push without committing (tree must already match task)

Requires:
  - On a run branch (experiment/sidecar-race--run-<id>-<arm>)
  - prep-check.sh --arm <arm> passes
  - ANTHROPIC_API_KEY (Claude Agent SDK; unless --replay-patches)
  - Sidecar arm: active sidecar; CIRCLE_TOKEN for epilogue
  - CI arm: CIRCLE_TOKEN; commits + push per task by default

Example (Cursor / terminal):
  cd experiments/sidecar-race
  ./scripts/prep-check.sh --arm sidecar
  ./scripts/ensure-sidecar.sh
  ./scripts/run-arm.sh --arm sidecar --notes "run 001"

Afterward:
  export RUN_ID=<id printed by new-run.sh>
  ./scripts/summarize-run.sh
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --arm) ARM="$2"; shift 2 ;;
    --run-id) RUN_ID="$2"; shift 2 ;;
    --from-task) FROM_TASK="$2"; shift 2 ;;
    --to-task) TO_TASK="$2"; shift 2 ;;
    --notes) NOTES="$2"; shift 2 ;;
    --dry-run) DRY_RUN=true; shift ;;
    --no-ensure-sidecar) ENSURE_SIDECAR=false; shift ;;
    --no-epilogue) EPILOGUE=false; shift ;;
    --no-commit) COMMIT_CI=false; shift ;;
    --replay-patches) REPLAY_PATCHES=true; shift ;;
    -h | --help) usage; exit 0 ;;
    *) die "unknown argument: $1" ;;
  esac
done

[[ -n "${ARM}" ]] || die "--arm is required (sidecar or ci)"
[[ "${ARM}" == "sidecar" || "${ARM}" == "ci" ]] || die "--arm must be sidecar or ci"

"${SCRIPT_DIR}/prep-check.sh" --arm "${ARM}"

branch="$(git -C "${REPO_ROOT}" branch --show-current)"
if [[ "${branch}" == "experiment/sidecar-race" || "${branch}" == "main" ]]; then
  die "checkout a run branch first, e.g. $(run_branch_example "${ARM}") (from experiment/sidecar-race)"
fi
if ! is_run_branch "${branch}"; then
  die "run branch must match experiment/sidecar-race--run-<id>-<arm>, got: ${branch}"
fi

if [[ "${ARM}" == "sidecar" && "${FROM_TASK}" -eq 1 && "${DRY_RUN}" != true ]]; then
  echo "Resetting repo working tree to HEAD (clean experiment state)..."
  git -C "${REPO_ROOT}" reset --hard HEAD
  git -C "${REPO_ROOT}" clean -fd internal/racefixture 2>/dev/null || true
fi

if [[ "${ARM}" == "sidecar" && "${ENSURE_SIDECAR}" == true ]]; then
  if [[ "${DRY_RUN}" == true ]]; then
    echo "[dry-run] ensure-sidecar.sh"
  else
    "${SCRIPT_DIR}/ensure-sidecar.sh"
  fi
  if [[ "${FROM_TASK}" -eq 1 && "${DRY_RUN}" != true ]]; then
    echo "Warming sidecar toolchain (sync + remote lint)..."
    chunk_in_repo sidecar sync
    chunk_in_repo validate --remote lint
  fi
fi

if [[ -n "${RUN_ID}" ]]; then
  export RUN_ID
  run_arm="$(read_run_field arm)"
  [[ "${run_arm}" == "${ARM}" ]] || die "RUN_ID=${RUN_ID} is arm=${run_arm}, not ${ARM}"
else
  if [[ "${DRY_RUN}" == true ]]; then
    echo "[dry-run] new-run.sh --arm ${ARM} --notes '${NOTES}'"
    RUN_ID="dry-run"
  else
    "${SCRIPT_DIR}/new-run.sh" --arm "${ARM}" --notes "${NOTES}"
    RUN_ID="$(read_run_field run_id)"
  fi
  export RUN_ID
fi

if [[ "${DRY_RUN}" != true ]]; then
  python3 - "${RUN_DIR:-$(resolve_run_dir)}/run.json" <<'PY'
import json, sys
from datetime import datetime, timezone
from pathlib import Path
p = Path(sys.argv[1])
meta = json.loads(p.read_text())
meta["run_wall_started_at"] = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
p.write_text(json.dumps(meta, indent=2) + "\n")
PY
fi

echo ""
echo "=== Running ${ARM} arm: tasks ${FROM_TASK}-${TO_TASK} (run_id=${RUN_ID}) ==="
echo ""

for ((iter = FROM_TASK; iter <= TO_TASK; iter++)); do
  echo "---------- Task ${iter} / ${TO_TASK} ----------"
  if [[ "${DRY_RUN}" == true ]]; then
    if [[ "${REPLAY_PATCHES}" == true ]]; then
      echo "[dry-run] apply-task.sh ${iter}"
    else
      echo "[dry-run] run-agent-task.sh ${iter}"
    fi
    if [[ "${ARM}" == "ci" && "${COMMIT_CI}" == true ]]; then
      echo "[dry-run] git commit task ${iter}"
    fi
    echo "[dry-run] ${ARM}-iter.sh ${iter}"
    continue
  fi

  if [[ "${REPLAY_PATCHES}" == true ]]; then
    "${SCRIPT_DIR}/apply-task.sh" "${iter}"
  else
    "${SCRIPT_DIR}/run-agent-task.sh" "${iter}"
  fi

  if [[ "${ARM}" == "ci" ]]; then
    if [[ "${COMMIT_CI}" == true ]]; then
      git -C "${REPO_ROOT}" add internal/racefixture 2>/dev/null || true
      if git -C "${REPO_ROOT}" diff --cached --quiet; then
        git -C "${REPO_ROOT}" add -u internal/racefixture internal/config/config_test.go 2>/dev/null || true
      fi
      if ! git -C "${REPO_ROOT}" diff --cached --quiet; then
        git -C "${REPO_ROOT}" commit -m "experiment: task ${iter}"
      else
        echo "warning: no staged changes for task ${iter}; pushing existing commit"
      fi
    fi
    "${SCRIPT_DIR}/ci-iter.sh" "${iter}"
  else
    "${SCRIPT_DIR}/sidecar-iter.sh" "${iter}"
  fi
done

if [[ "${ARM}" == "sidecar" && "${EPILOGUE}" == true && "${DRY_RUN}" != true ]]; then
  echo ""
  echo "=== Sidecar epilogue: final push + CI validation ==="
  "${SCRIPT_DIR}/sidecar-epilogue.sh" --to-task "${TO_TASK}" --notes "${NOTES}"
fi

if [[ "${DRY_RUN}" != true ]]; then
  python3 - "$(resolve_run_dir)/run.json" <<'PY'
import json, sys
from datetime import datetime, timezone
from pathlib import Path
p = Path(sys.argv[1])
meta = json.loads(p.read_text())
meta["run_wall_ended_at"] = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
p.write_text(json.dumps(meta, indent=2) + "\n")
PY
  "${SCRIPT_DIR}/finalize-metrics.sh"
  echo ""
  "${SCRIPT_DIR}/summarize-run.sh"
  echo ""
  RUN_LABEL="$(run_label_from_branch "${branch}" "${ARM}")"
  if command -v gh >/dev/null 2>&1 && gh auth status >/dev/null 2>&1; then
    echo "Updating run PR (metrics + ready for review)..."
    "${SCRIPT_DIR}/open-run-pr.sh" --run-id "${RUN_LABEL}" --arm "${ARM}" --update --commit-results \
      || echo "warning: could not update run PR"
  else
    echo "Skipping run PR update: install and authenticate gh, or run open-run-pr.sh --update manually"
  fi
  echo ""
  echo "Done. Results: $(resolve_run_dir)/results.csv"
fi

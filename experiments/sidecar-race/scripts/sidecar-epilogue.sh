#!/usr/bin/env bash
# After a sidecar arm: commit cumulative task state, push, poll CI gates + full workflow.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/common.sh
source "${SCRIPT_DIR}/lib/common.sh"

NOTES="final ci validation"
SKIP_PUSH=false
SKIP_COMMIT=false
TO_TASK=10

usage() {
  cat <<EOF
Usage: sidecar-epilogue.sh [options]

Run after sidecar-iter tasks 1-${TO_TASK} to verify pipeline confidence on GitHub.

  1. Commit cumulative experiment tree (tasks 1-${TO_TASK})
  2. git push
  3. Poll CircleCI gate jobs (lint, test)
  4. Poll full ci workflow (all jobs)
  5. Write epilogue.json and append results.csv row (iter=epilogue)

Requires: RUN_ID (sidecar arm), CIRCLE_TOKEN, on experiment/sidecar-race--run-*-sidecar branch.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --notes) NOTES="$2"; shift 2 ;;
    --skip-push) SKIP_PUSH=true; shift ;;
    --skip-commit) SKIP_COMMIT=true; shift ;;
    --to-task) TO_TASK="$2"; shift 2 ;;
    -h | --help) usage; exit 0 ;;
    *) die "unknown argument: $1" ;;
  esac
done

CIRCLE_TOKEN="${CIRCLE_TOKEN:-${CIRCLECI_TOKEN:-}}"
: "${CIRCLE_TOKEN:?CIRCLE_TOKEN is required}"

ARM="$(read_run_field arm)"
[[ "${ARM}" == "sidecar" ]] || die "run arm is '${ARM}', expected sidecar"

RUN_ID="$(read_run_field run_id)"
BRANCH="$(git -C "${REPO_ROOT}" branch --show-current)"
is_run_branch "${BRANCH}" || die "not on a run branch: ${BRANCH}"

STARTED="$(iso_timestamp)"
START_EPOCH="$(epoch_seconds)"
export START_EPOCH

RUN_DIR="$(resolve_run_dir)"
EPILOGUE_JSON="${RUN_DIR}/epilogue.json"

echo "Verifying epilogue tree passes local CI gates (shellcheck, lint, test)..."
"${SCRIPT_DIR}/verify-epilogue-ready.sh" --to-task "${TO_TASK}"

if [[ "${SKIP_COMMIT}" != true ]]; then
  echo "Committing cumulative task 1-${TO_TASK} state for CI epilogue..."
  git -C "${REPO_ROOT}" add internal/racefixture 2>/dev/null || true
  git -C "${REPO_ROOT}" add -u internal/racefixture internal/config 2>/dev/null || true
  if ! git -C "${REPO_ROOT}" diff --cached --quiet; then
    git -C "${REPO_ROOT}" commit -m "experiment: tasks 1-${TO_TASK} (epilogue ci push)"
  else
    echo "warning: no staged changes; using existing commits for push"
  fi
fi

if [[ "${SKIP_PUSH}" != true ]]; then
  echo "Pushing ${BRANCH} to origin..."
  git -C "${REPO_ROOT}" push -u origin "${BRANCH}"
  sleep 5
fi

echo "Polling CI gate jobs (lint, test)..."
GATE_CSV="$("${SCRIPT_DIR}/poll-ci-gate.sh" --branch "${BRANCH}")"
IFS=',' read -r PIPELINE_ID WORKFLOW_ID LINT_JOB TEST_JOB LINT_OK TEST_OK LINT_DUR TEST_DUR _GATE_TTS WF_CRED GATE_CRED CI_COST <<<"${GATE_CSV}"

echo "Polling full ci workflow..."
WF_TMP="${EPILOGUE_JSON}.tmp"
"${SCRIPT_DIR}/poll-ci-workflow.sh" --branch "${BRANCH}" --pipeline-id "${PIPELINE_ID}" --output "${WF_TMP}"
python3 - "${EPILOGUE_JSON}" "${WF_TMP}" "${GATE_CSV}" "${STARTED}" "${BRANCH}" <<'PY'
import json
import sys
from pathlib import Path

out = Path(sys.argv[1])
wf_path = Path(sys.argv[2])
gate_csv = sys.argv[3].split(",")
started = sys.argv[4]
branch = sys.argv[5]
wf = json.loads(wf_path.read_text())
epilogue = {
    "kind": "sidecar_ci_epilogue",
    "started_at": started,
    "branch": branch,
    "gate": {
        "pipeline_id": gate_csv[0],
        "workflow_id": gate_csv[1],
        "lint_job_num": gate_csv[2],
        "test_job_num": gate_csv[3],
        "lint_ok": gate_csv[4],
        "test_ok": gate_csv[5],
        "lint_duration_s": int(gate_csv[6] or 0),
        "test_duration_s": int(gate_csv[7] or 0),
        "tts_seconds": int(gate_csv[8] or 0),
        "ci_workflow_credits": int(gate_csv[9] or 0) if len(gate_csv) > 9 else 0,
        "ci_gate_credits": int(gate_csv[10] or 0) if len(gate_csv) > 10 else 0,
        "ci_cost_usd": float(gate_csv[11] or 0) if len(gate_csv) > 11 else 0,
    },
    "workflow": wf,
}
wf_path.unlink(missing_ok=True)
out.write_text(json.dumps(epilogue, indent=2) + "\n")
PY

ENDED="$(iso_timestamp)"
END_EPOCH="$(epoch_seconds)"
TTS=$((END_EPOCH - START_EPOCH))
SHA="$(git -C "${REPO_ROOT}" rev-parse HEAD)"

WF_OK="$(python3 -c "import json; print('pass' if json.load(open('${EPILOGUE_JSON}'))['workflow'].get('workflow_ok') else 'fail')")"

init_csv_if_missing
WF_CRED="$(python3 -c "import json; print(json.load(open('${EPILOGUE_JSON}')).get('workflow',{}).get('ci_workflow_credits',0))")"
CI_COST="$(python3 -c "import json; print(json.load(open('${EPILOGUE_JSON}')).get('workflow',{}).get('ci_cost_usd',0))")"
GATE_CRED="$(python3 -c "import json; print(json.load(open('${EPILOGUE_JSON}'))['gate'].get('ci_gate_credits',0))")"

append_csv_row \
  "sidecar,${RUN_ID},epilogue,${STARTED},${ENDED},${TTS},${LINT_OK},${TEST_OK},${LINT_DUR},${TEST_DUR},,${PIPELINE_ID},${WORKFLOW_ID},${LINT_JOB},${TEST_JOB},${SHA},${NOTES} (workflow=${WF_OK}),${WF_CRED},${GATE_CRED},${CI_COST},,,0,0"

# Patch run.json
python3 - "${RUN_DIR}/run.json" "${EPILOGUE_JSON}" <<'PY'
import json
import sys
from pathlib import Path

meta_path = Path(sys.argv[1])
epilogue_path = Path(sys.argv[2])
meta = json.loads(meta_path.read_text())
meta["epilogue"] = json.loads(epilogue_path.read_text())
meta_path.write_text(json.dumps(meta, indent=2) + "\n")
PY

echo ""
echo "Epilogue complete: gate lint=${LINT_OK} test=${TEST_OK} workflow=${WF_OK} tts=${TTS}s"
echo "  ${EPILOGUE_JSON}"
echo "  pipeline: https://app.circleci.com/pipelines/github/CircleCI-Public/chunk-cli/${PIPELINE_ID}"

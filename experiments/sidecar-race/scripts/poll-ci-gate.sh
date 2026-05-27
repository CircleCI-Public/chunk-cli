#!/usr/bin/env bash
# Poll CircleCI until lint and test jobs finish for a pipeline on a branch.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/common.sh
source "${SCRIPT_DIR}/lib/common.sh"

BRANCH=""
PIPELINE_ID=""
POLL_INTERVAL="${POLL_INTERVAL:-15}"
MAX_WAIT="${MAX_WAIT:-900}"
PROJECT_SLUG="${CIRCLE_PROJECT_SLUG:-github/CircleCI-Public/chunk-cli}"

CIRCLE_TOKEN="${CIRCLE_TOKEN:-${CIRCLECI_TOKEN:-}}"
: "${CIRCLE_TOKEN:?CIRCLE_TOKEN is required}"

usage() {
  cat <<EOF
Usage: poll-ci-gate.sh [--branch <name>] [--pipeline-id <id>]

Waits for lint and test jobs. Writes fields to stdout as shell assignments and
a CSV fragment on the last line (pipeline_id,workflow_id,...).

Environment:
  START_EPOCH     Wall-clock start for TTS (set by ci-iter.sh before push)
  POLL_INTERVAL   Seconds between polls (default 15)
  MAX_WAIT        Max seconds to wait (default 900)
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --branch) BRANCH="$2"; shift 2 ;;
    --pipeline-id) PIPELINE_ID="$2"; shift 2 ;;
    -h | --help) usage; exit 0 ;;
    *) die "unknown argument: $1" ;;
  esac
done

if [[ -z "${BRANCH}" ]]; then
  BRANCH="$(git -C "${REPO_ROOT}" branch --show-current)"
fi

export CIRCLE_TOKEN PROJECT_SLUG BRANCH PIPELINE_ID POLL_INTERVAL MAX_WAIT
export START_EPOCH="${START_EPOCH:-$(epoch_seconds)}"
export METRICS_LIB="${SCRIPT_DIR}/lib"

RESULT_FILE="$(mktemp)"
trap 'rm -f "${RESULT_FILE}"' EXIT

python3 - "${RESULT_FILE}" <<'PY'
import json
import os
import subprocess
import sys
import time
from pathlib import Path

out_path = Path(sys.argv[1])
token = os.environ["CIRCLE_TOKEN"]
slug = os.environ["PROJECT_SLUG"]
branch = os.environ["BRANCH"]
pipeline_id = os.environ.get("PIPELINE_ID") or ""
poll = int(os.environ.get("POLL_INTERVAL", "15"))
max_wait = int(os.environ.get("MAX_WAIT", "900"))
start = int(os.environ.get("START_EPOCH", str(int(time.time()))))


def api(path: str) -> dict:
    raw = subprocess.check_output(
        [
            "curl", "-fsSL",
            "-H", f"Circle-Token: {token}",
            f"https://circleci.com/api/v2{path}",
        ],
    )
    return json.loads(raw)


if not pipeline_id:
    data = api(f"/project/{slug}/pipeline?branch={branch}")
    items = data.get("items") or []
    if not items:
        raise SystemExit(f"no pipelines found for branch {branch}")
    pipeline_id = items[0]["id"]

deadline = time.time() + max_wait
workflow_id = ""
workflow_status = ""
while time.time() < deadline:
    wfs = api(f"/pipeline/{pipeline_id}/workflow").get("items") or []
    if wfs:
        workflow_id = wfs[0]["id"]
        workflow_status = wfs[0].get("status") or ""
        if workflow_status in ("success", "failed", "canceled", "error"):
            break
    time.sleep(poll)

if not workflow_id:
    raise SystemExit("no workflow found for pipeline")

gate = ("lint", "test")
jobs: dict[str, dict] = {}
while time.time() < deadline:
    for j in api(f"/workflow/{workflow_id}/job").get("items") or []:
        name = j.get("name")
        if name in gate:
            jobs[name] = j
    if all(jobs.get(n, {}).get("status") in ("success", "failed", "canceled") for n in gate):
        break
    time.sleep(poll)

missing = [n for n in gate if n not in jobs]
if missing:
    raise SystemExit(f"timed out or missing jobs: {missing}")

def ok(status: str | None) -> str:
    return "pass" if status == "success" else "fail"

lint = jobs["lint"]
test = jobs["test"]
tts = int(time.time()) - start

import sys

sys.path.insert(0, os.environ.get("METRICS_LIB", ""))
from metrics import credits_to_usd, gate_job_credits, job_duration_seconds  # noqa: E402

lint_dur = round(job_duration_seconds(lint), 1)
test_dur = round(job_duration_seconds(test), 1)

wf_credits, lint_credits, test_credits = gate_job_credits(
    slug, workflow_id, lint_dur, test_dur
)
gate_credits = lint_credits + test_credits
ci_cost = credits_to_usd(wf_credits)

result = {
    "pipeline_id": pipeline_id,
    "workflow_id": workflow_id,
    "lint_job_num": lint.get("job_number", ""),
    "test_job_num": test.get("job_number", ""),
    "lint_ok": ok(lint.get("status")),
    "test_ok": ok(test.get("status")),
    "lint_duration_s": lint_dur,
    "test_duration_s": test_dur,
    "tts_seconds": tts,
    "ci_workflow_credits": wf_credits,
    "ci_gate_credits": gate_credits,
    "ci_cost_usd": ci_cost,
}
out_path.write_text(json.dumps(result))
print(
    f"{pipeline_id},{workflow_id},{result['lint_job_num']},{result['test_job_num']},"
    f"{result['lint_ok']},{result['test_ok']},{lint_dur},{test_dur},{tts},"
    f"{wf_credits},{gate_credits},{ci_cost}"
)
PY

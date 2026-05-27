#!/usr/bin/env bash
# Poll CircleCI until the full "ci" workflow finishes; record all workflow jobs.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/common.sh
source "${SCRIPT_DIR}/lib/common.sh"

BRANCH=""
PIPELINE_ID=""
WORKFLOW_NAME="${WORKFLOW_NAME:-ci}"
POLL_INTERVAL="${POLL_INTERVAL:-15}"
MAX_WAIT="${MAX_WAIT:-1800}"
PROJECT_SLUG="${CIRCLE_PROJECT_SLUG:-github/CircleCI-Public/chunk-cli}"
OUTPUT=""

CIRCLE_TOKEN="${CIRCLE_TOKEN:-${CIRCLECI_TOKEN:-}}"
: "${CIRCLE_TOKEN:?CIRCLE_TOKEN is required}"

usage() {
  cat <<EOF
Usage: poll-ci-workflow.sh [--branch <name>] [--pipeline-id <id>] [--output <file.json>]

Waits for workflow "${WORKFLOW_NAME}" and all its jobs to reach a terminal state.
Writes JSON to --output (or stdout if omitted).

Environment:
  START_EPOCH     Wall-clock start for TTS (default: now)
  POLL_INTERVAL   Seconds between polls (default 15)
  MAX_WAIT        Max seconds to wait (default 1800)
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --branch) BRANCH="$2"; shift 2 ;;
    --pipeline-id) PIPELINE_ID="$2"; shift 2 ;;
    --output) OUTPUT="$2"; shift 2 ;;
    -h | --help) usage; exit 0 ;;
    *) die "unknown argument: $1" ;;
  esac
done

if [[ -z "${BRANCH}" ]]; then
  BRANCH="$(git -C "${REPO_ROOT}" branch --show-current)"
fi

export CIRCLE_TOKEN PROJECT_SLUG BRANCH PIPELINE_ID WORKFLOW_NAME POLL_INTERVAL MAX_WAIT
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
workflow_name = os.environ.get("WORKFLOW_NAME", "ci")
poll = int(os.environ.get("POLL_INTERVAL", "15"))
max_wait = int(os.environ.get("MAX_WAIT", "1800"))
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


def terminal(status: str | None) -> bool:
    return status in ("success", "failed", "canceled", "error", "not_run", "skipped")


if not pipeline_id:
    data = api(f"/project/{slug}/pipeline?branch={branch}")
    items = data.get("items") or []
    if not items:
        raise SystemExit(f"no pipelines found for branch {branch}")
    pipeline_id = items[0]["id"]

deadline = time.time() + max_wait
workflow_id = ""
workflow_status = ""
workflow_created = ""
while time.time() < deadline:
    wfs = api(f"/pipeline/{pipeline_id}/workflow").get("items") or []
    match = next((w for w in wfs if w.get("name") == workflow_name), None)
    if match:
        workflow_id = match["id"]
        workflow_status = match.get("status") or ""
        workflow_created = match.get("created_at") or ""
        if terminal(workflow_status):
            break
    time.sleep(poll)

if not workflow_id:
    raise SystemExit(f"no workflow named {workflow_name!r} found for pipeline {pipeline_id}")

jobs: dict[str, dict] = {}
while time.time() < deadline:
    items = api(f"/workflow/{workflow_id}/job").get("items") or []
    for j in items:
        name = j.get("name")
        if name:
            jobs[name] = {
                "job_number": j.get("job_number"),
                "status": j.get("status"),
                "started_at": j.get("started_at"),
                "stopped_at": j.get("stopped_at"),
            }
    if items and all(terminal(j.get("status")) for j in items):
        break
    time.sleep(poll)

tts = int(time.time()) - start
workflow_ok = workflow_status == "success"

def job_ok(name: str) -> str:
    st = jobs.get(name, {}).get("status")
    return "pass" if st == "success" else "fail"

import sys

sys.path.insert(0, os.environ.get("METRICS_LIB", ""))
from metrics import credits_to_usd, enrich_workflow_jobs, job_duration_seconds  # noqa: E402

for name, info in jobs.items():
    info["duration_s"] = round(job_duration_seconds(info), 1)

wf_credits, jobs = enrich_workflow_jobs(slug, workflow_id, jobs)

result = {
    "pipeline_id": pipeline_id,
    "workflow_id": workflow_id,
    "workflow_name": workflow_name,
    "workflow_status": workflow_status,
    "workflow_ok": workflow_ok,
    "workflow_duration_s": tts,
    "tts_seconds": tts,
    "ci_workflow_credits": wf_credits,
    "ci_cost_usd": credits_to_usd(wf_credits),
    "jobs": jobs,
    "shellcheck_ok": job_ok("shellcheck/check"),
    "lint_ok": job_ok("lint"),
    "test_ok": job_ok("test"),
    "acceptance_ok": job_ok("acceptance-test"),
    "build_smoke_ok": job_ok("build-smoke-test"),
}
out_path.write_text(json.dumps(result, indent=2) + "\n")
print(json.dumps(result))
PY

if [[ -n "${OUTPUT}" ]]; then
  cp "${RESULT_FILE}" "${OUTPUT}"
  echo "Wrote ${OUTPUT}"
else
  cat "${RESULT_FILE}"
fi

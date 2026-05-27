#!/usr/bin/env bash
# Re-fetch job durations/credits for an existing epilogue.json (fixes pre-fix zero durations).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/common.sh
source "${SCRIPT_DIR}/lib/common.sh"

CIRCLE_TOKEN="${CIRCLE_TOKEN:-${CIRCLECI_TOKEN:-}}"
: "${CIRCLE_TOKEN:?CIRCLE_TOKEN is required}"

RUN_DIR="$(resolve_run_dir)"
EPILOGUE="${RUN_DIR}/epilogue.json"
[[ -f "${EPILOGUE}" ]] || die "missing ${EPILOGUE}"

export METRICS_LIB="${SCRIPT_DIR}/lib"
PROJECT_SLUG="${CIRCLE_PROJECT_SLUG:-github/CircleCI-Public/chunk-cli}"

python3 - "${EPILOGUE}" "${PROJECT_SLUG}" <<'PY'
import json
import os
import sys
from pathlib import Path

sys.path.insert(0, os.environ["METRICS_LIB"])
from metrics import (  # noqa: E402
    credits_to_usd,
    enrich_workflow_jobs,
    gate_job_credits,
    job_duration_seconds,
)

ep_path = Path(sys.argv[1])
slug = sys.argv[2]
ep = json.loads(ep_path.read_text())

wf = ep.get("workflow") or {}
wf_id = wf.get("workflow_id") or (ep.get("gate") or {}).get("workflow_id")
if not wf_id:
    raise SystemExit("no workflow_id in epilogue.json")

jobs = wf.get("jobs") or {}
wf_credits, jobs = enrich_workflow_jobs(slug, wf_id, jobs)
wf["jobs"] = jobs
wf["ci_workflow_credits"] = wf_credits
wf["ci_cost_usd"] = credits_to_usd(wf_credits)

gate = ep.get("gate") or {}
lint_d = gate.get("lint_duration_s") or 0
test_d = gate.get("test_duration_s") or 0
if lint_d == 0 and test_d == 0:
    lint_job = jobs.get("lint") or {}
    test_job = jobs.get("test") or {}
    lint_d = lint_job.get("duration_s") or 0
    test_d = test_job.get("duration_s") or 0
    gate["lint_duration_s"] = lint_d
    gate["test_duration_s"] = test_d

total, lint_c, test_c = gate_job_credits(slug, wf_id, float(lint_d), float(test_d))
gate["ci_workflow_credits"] = total
gate["ci_gate_credits"] = lint_c + test_c
gate["ci_cost_usd"] = credits_to_usd(total)
ep["gate"] = gate
ep["workflow"] = wf

ep_path.write_text(json.dumps(ep, indent=2) + "\n")
print(f"Updated {ep_path}")
for name, info in sorted(jobs.items()):
    print(
        f"  {name}: {info.get('duration_s')}s, "
        f"{info.get('credits_est')} credits, {info.get('cost_usd_est')}"
    )
PY

"${SCRIPT_DIR}/finalize-metrics.sh"
echo "Re-run PR body: ./scripts/open-run-pr.sh --run-id <label> --arm sidecar --update"

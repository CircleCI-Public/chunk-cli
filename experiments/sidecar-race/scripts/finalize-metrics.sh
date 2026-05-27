#!/usr/bin/env bash
# Backfill CSV cost columns, allocate sidecar credits, write costs_summary.json.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/common.sh
source "${SCRIPT_DIR}/lib/common.sh"

export METRICS_LIB="${SCRIPT_DIR}/lib"
RUN_DIR="$(resolve_run_dir)"
CSV="${RUN_DIR}/results.csv"
META="$(run_json)"
COSTS="${RUN_DIR}/costs_summary.json"

python3 - "${CSV}" "${META}" "${COSTS}" "${METRICS_LIB}" <<'PY'
import csv
import json
import sys
from datetime import datetime, timezone
from pathlib import Path

sys.path.insert(0, sys.argv[4])
from llm_usage import format_usd, load_llm_totals  # noqa: E402
from metrics import (  # noqa: E402
    credits_to_usd,
    gate_job_credits,
    sidecar_credits_per_min,
    workflow_total_credits_from_jobs,
)

csv_path = Path(sys.argv[1])
meta_path = Path(sys.argv[2])
costs_path = Path(sys.argv[3])
meta = json.loads(meta_path.read_text())
slug = "github/CircleCI-Public/chunk-cli"

rows = list(csv.DictReader(csv_path.open()))
fieldnames = list(rows[0].keys()) if rows else []

cost_cols = [
    "ci_workflow_credits",
    "ci_gate_credits",
    "ci_cost_usd",
    "sidecar_credits_est",
    "sidecar_cost_usd",
    "llm_tokens",
    "llm_cost_usd",
]
for c in cost_cols:
    if c not in fieldnames:
        fieldnames.append(c)

def to_int(v, default=0):
    try:
        return int(v or 0)
    except ValueError:
        return default

def to_float(v, default=0.0):
    try:
        return float(v or default)
    except ValueError:
        return default

# Backfill CI credits from workflow_id when missing.
for r in rows:
    if not r.get("llm_tokens"):
        r["llm_tokens"] = ""
        r["llm_cost_usd"] = ""
    wf = r.get("ci_workflow_id") or ""
    if wf and not to_int(r.get("ci_workflow_credits")):
        lint_d = to_int(r.get("lint_duration_s"))
        test_d = to_int(r.get("test_duration_s"))
        total, lint_c, test_c = gate_job_credits(slug, wf, lint_d, test_d)
        r["ci_workflow_credits"] = str(total)
        r["ci_gate_credits"] = str(lint_c + test_c)
        r["ci_cost_usd"] = str(credits_to_usd(total))

# Sidecar credit allocation by iteration wall time (tts).
sidecar_rate = sidecar_credits_per_min()
iter_rows = [r for r in rows if r.get("iter") not in ("epilogue", "") and r.get("iter", "").isdigit()]
if meta.get("arm") == "sidecar" and sidecar_rate > 0 and iter_rows:
  started = meta.get("run_wall_started_at") or meta.get("created_at")
  ended = meta.get("run_wall_ended_at") or datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
  try:
      t0 = datetime.fromisoformat(started.replace("Z", "+00:00"))
      t1 = datetime.fromisoformat(ended.replace("Z", "+00:00"))
      session_min = max((t1 - t0).total_seconds() / 60.0, 0.0)
  except ValueError:
      session_min = sum(to_int(r.get("tts_seconds")) for r in iter_rows) / 60.0
  total_sidecar_credits = int(session_min * sidecar_rate)
  tts_sum = sum(to_int(r.get("tts_seconds")) for r in iter_rows) or 1
  for r in iter_rows:
      share = to_int(r.get("tts_seconds")) / tts_sum
      cred = int(total_sidecar_credits * share)
      r["sidecar_credits_est"] = str(cred)
      r["sidecar_cost_usd"] = str(credits_to_usd(cred))

# Epilogue workflow job breakdown from epilogue.json
epilogue_path = meta_path.parent / "epilogue.json"
workflow_jobs = {}
if epilogue_path.exists():
    ep = json.loads(epilogue_path.read_text())
    wf = ep.get("workflow") or {}
    workflow_jobs = wf.get("jobs") or {}
    gate = ep.get("gate") or {}
    for r in rows:
        if r.get("iter") == "epilogue":
            if not to_int(r.get("ci_workflow_credits")):
                wf_id = gate.get("workflow_id") or r.get("ci_workflow_id") or ""
                if wf_id:
                    total, _, _ = gate_job_credits(
                        slug,
                        wf_id,
                        gate.get("lint_duration_s", 0),
                        gate.get("test_duration_s", 0),
                    )
                    r["ci_workflow_credits"] = str(total)
                    r["ci_gate_credits"] = str(
                        gate.get("lint_duration_s", 0) + gate.get("test_duration_s", 0)
                    )
                    r["ci_cost_usd"] = str(credits_to_usd(total))

with csv_path.open("w", newline="") as f:
    w = csv.DictWriter(f, fieldnames=fieldnames, extrasaction="ignore")
    w.writeheader()
    w.writerows(rows)

def sum_col(name):
    return sum(to_int(r.get(name)) for r in rows)

def sum_float(name):
    return round(sum(to_float(r.get(name)) for r in rows), 4)

llm = load_llm_totals(meta_path.parent)
summary = {
    "run_id": meta.get("run_id"),
    "arm": meta.get("arm"),
    "credit_usd_rate": float(__import__("os").environ.get("CIRCLECI_CREDIT_USD", "0.0006")),
    "sidecar_credits_per_min": sidecar_rate,
    "llm_measured": llm["measured"],
    "llm_note": llm.get("note"),
    "llm_source": llm.get("source"),
    "totals": {
        "iterations": len(iter_rows),
        "tts_seconds_sum": sum(to_int(r.get("tts_seconds")) for r in iter_rows),
        "ci_workflow_credits_sum": sum_col("ci_workflow_credits"),
        "ci_gate_credits_sum": sum_col("ci_gate_credits"),
        "ci_cost_usd_sum": sum_float("ci_cost_usd"),
        "ci_cost_display": format_usd(sum_float("ci_cost_usd")),
        "sidecar_credits_est_sum": sum_col("sidecar_credits_est"),
        "sidecar_cost_usd_sum": sum_float("sidecar_cost_usd"),
        "sidecar_cost_display": format_usd(sum_float("sidecar_cost_usd")),
        "llm_input_tokens": llm.get("input_tokens"),
        "llm_output_tokens": llm.get("output_tokens"),
        "llm_tokens_sum": llm.get("total_tokens"),
        "llm_cost_usd_sum": llm.get("cost_usd"),
        "llm_cost_display": format_usd(llm.get("cost_usd")),
    },
    "epilogue_workflow_jobs": workflow_jobs,
}
costs_path.write_text(json.dumps(summary, indent=2) + "\n")
print(json.dumps(summary, indent=2))
PY

echo "Wrote ${COSTS}"

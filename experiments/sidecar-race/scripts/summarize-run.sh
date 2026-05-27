#!/usr/bin/env bash
# Print summary statistics for a run's results.csv.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/common.sh
source "${SCRIPT_DIR}/lib/common.sh"

RUN_DIR="$(resolve_run_dir)"
CSV="${RUN_DIR}/results.csv"
OUT="${RUN_DIR}/summary.txt"

[[ -f "${CSV}" ]] || die "no results.csv in ${RUN_DIR}"

python3 - "${CSV}" "${OUT}" "$(run_json)" <<'PY'
import csv
import json
import statistics
import sys
from pathlib import Path

csv_path = Path(sys.argv[1])
out_path = Path(sys.argv[2])
meta_path = Path(sys.argv[3])

meta = json.loads(meta_path.read_text())
rows = list(csv.DictReader(csv_path.open()))
iter_rows = [r for r in rows if r.get("iter") != "epilogue"]
epilogue_rows = [r for r in rows if r.get("iter") == "epilogue"]

tts = [int(r["tts_seconds"]) for r in iter_rows if r.get("tts_seconds", "").isdigit()]

def stats(vals):
    if not vals:
        return {}
    return {
        "n": len(vals),
        "median_s": statistics.median(vals),
        "p95_s": sorted(vals)[max(0, int(len(vals) * 0.95) - 1)],
        "min_s": min(vals),
        "max_s": max(vals),
    }

lines = [
    f"Sidecar race run summary: {meta.get('run_id')}",
    f"  arm:     {meta.get('arm')}",
    f"  branch:  {meta.get('branch')}",
    f"  git_sha: {meta.get('git_sha')}",
    f"  notes:   {meta.get('notes', '')}",
    "",
    f"Iterations recorded: {len(iter_rows)}",
    f"Time to signal (seconds): {json.dumps(stats(tts), indent=2)}",
    "",
]

if iter_rows:
    lines.append("Per iteration:")
    for r in iter_rows:
        lines.append(
            f"  iter {r.get('iter')}: tts={r.get('tts_seconds')}s "
            f"lint={r.get('lint_ok')} test={r.get('test_ok')}"
        )

costs_path = meta_path.parent / "costs_summary.json"
if costs_path.exists():
    costs = json.loads(costs_path.read_text())
    lines.extend([
        "",
        "Costs (see costs_summary.json):",
        f"  CI credits (sum):     {costs.get('totals', {}).get('ci_workflow_credits_sum')}",
        f"  CI cost (sum):        {costs.get('totals', {}).get('ci_cost_display', costs.get('totals', {}).get('ci_cost_usd_sum'))}",
        f"  Sidecar credits est:  {costs.get('totals', {}).get('sidecar_credits_est_sum')}",
        f"  Sidecar cost (est.):  {costs.get('totals', {}).get('sidecar_cost_display', costs.get('totals', {}).get('sidecar_cost_usd_sum'))}",
        f"  LLM tokens:           {costs.get('totals', {}).get('llm_tokens_sum') if costs.get('llm_measured') else 'n/a'}",
        f"  LLM cost:             {costs.get('totals', {}).get('llm_cost_display', 'n/a')}",
    ])

epilogue_path = meta_path.parent / "epilogue.json"
if epilogue_rows:
    e = epilogue_rows[0]
    lines.extend([
        "",
        "Epilogue (final push → CI):",
        f"  tts={e.get('tts_seconds')}s gate lint={e.get('lint_ok')} test={e.get('test_ok')}",
    ])
if epilogue_path.exists():
    ep = json.loads(epilogue_path.read_text())
    wf = ep.get("workflow", {})
    lines.append(
        f"  workflow={wf.get('workflow_status')} "
        f"shellcheck={wf.get('shellcheck_ok')} acceptance={wf.get('acceptance_ok')} "
        f"build-smoke={wf.get('build_smoke_ok')}"
    )

text = "\n".join(lines) + "\n"
out_path.write_text(text)
print(text, end="")
PY

echo "Wrote ${OUT}"

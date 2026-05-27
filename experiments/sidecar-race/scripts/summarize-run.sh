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

def col(name):
    return [r for r in rows if r.get(name)]

tts = [int(r["tts_seconds"]) for r in rows if r.get("tts_seconds", "").isdigit()]

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

lint_agree = sum(
    1 for r in rows
    if r.get("lint_ok") in ("pass", "fail") and r.get("test_ok") in ("pass", "fail")
)

lines = [
    f"Sidecar race run summary: {meta.get('run_id')}",
    f"  arm:     {meta.get('arm')}",
    f"  branch:  {meta.get('branch')}",
    f"  git_sha: {meta.get('git_sha')}",
    f"  notes:   {meta.get('notes', '')}",
    "",
    f"Iterations recorded: {len(rows)}",
    f"Time to signal (seconds): {json.dumps(stats(tts), indent=2)}",
    "",
]

if rows:
    lines.append("Per iteration:")
    for r in rows:
        lines.append(
            f"  iter {r.get('iter')}: tts={r.get('tts_seconds')}s "
            f"lint={r.get('lint_ok')} test={r.get('test_ok')}"
        )

text = "\n".join(lines) + "\n"
out_path.write_text(text)
print(text, end="")
PY

echo "Wrote ${OUT}"

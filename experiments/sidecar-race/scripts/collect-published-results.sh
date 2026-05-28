#!/usr/bin/env bash
# Copy committed run artifacts from origin run branches into results/published/.
# Run branches were deleted after May 2026 consolidation; re-collect only if branches exist.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/common.sh
source "${SCRIPT_DIR}/lib/common.sh"

LABELS="${SIDECAR_RACE_LABELS:-001,002,003,004,005}"
ARMS="${SIDECAR_RACE_ARMS:-sidecar,ci}"
PUBLISHED_ROOT="${EXPERIMENT_ROOT}/results/published"
INDEX_PATH="${PUBLISHED_ROOT}/index.json"

usage() {
  cat <<EOF
Usage: collect-published-results.sh [options]

Copy results from origin/experiment/sidecar-race--run-<label>-<arm> into
results/published/<label>-<arm>/ and write index.json.

Options:
  --labels <list>   Comma-separated labels (default: 001,002,003,004,005)
  --dry-run         Print actions without writing files
EOF
}

DRY_RUN=false
while [[ $# -gt 0 ]]; do
  case "$1" in
    --labels) LABELS="$2"; shift 2 ;;
    --dry-run) DRY_RUN=true; shift ;;
    -h | --help) usage; exit 0 ;;
    *) die "unknown argument: $1" ;;
  esac
done

IFS=',' read -r -a label_arr <<<"${LABELS}"
IFS=',' read -r -a arm_arr <<<"${ARMS}"

pr_for_branch() {
  local branch="$1"
  gh pr list --head "${branch}" --state all --json number --jq '.[0].number // empty' 2>/dev/null || true
}

mkdir -p "${PUBLISHED_ROOT}"
entries=()

for label in "${label_arr[@]}"; do
  label="$(echo "${label}" | xargs)"
  [[ -z "${label}" ]] && continue
  for arm in "${arm_arr[@]}"; do
    arm="$(echo "${arm}" | xargs)"
    [[ -z "${arm}" ]] && continue
    branch="experiment/sidecar-race--run-${label}-${arm}"
    ref="origin/${branch}"
    if ! git -C "${REPO_ROOT}" rev-parse --verify "${ref}" >/dev/null 2>&1; then
      echo "warn: skip ${label}-${arm}: missing ${ref}" >&2
      continue
    fi
    run_json_rel="$(
      git -C "${REPO_ROOT}" ls-tree -r --name-only "${ref}" experiments/sidecar-race/results 2>/dev/null \
        | grep '/run\.json$' | head -1
    )"
    if [[ -z "${run_json_rel}" ]]; then
      echo "warn: skip ${label}-${arm}: no run.json on ${ref}" >&2
      continue
    fi
    run_id="$(basename "$(dirname "${run_json_rel}")")"
    dest="${PUBLISHED_ROOT}/${label}-${arm}"
    files=(
      run.json
      results.csv
      costs_summary.json
      llm_usage.json
      metrics.jsonl
      agent_usage.jsonl
      summary.txt
    )
    if [[ "${arm}" == "sidecar" ]]; then
      files+=(epilogue.json)
    fi
    if [[ "${DRY_RUN}" == true ]]; then
      echo "would copy ${ref}:${run_id} -> ${dest}/"
      continue
    fi
    rm -rf "${dest}"
    mkdir -p "${dest}"
    for name in "${files[@]}"; do
      rel="experiments/sidecar-race/results/${run_id}/${name}"
      if git -C "${REPO_ROOT}" cat-file -e "${ref}:${rel}" 2>/dev/null; then
        git -C "${REPO_ROOT}" show "${ref}:${rel}" >"${dest}/${name}"
      fi
    done
    pr_num="$(pr_for_branch "${branch}")"
    entries+=("{\"label\":\"${label}\",\"arm\":\"${arm}\",\"run_id\":\"${run_id}\",\"branch\":\"${branch}\",\"pr\":${pr_num:-null}}")
    echo "collected ${label}-${arm} (${run_id}) from ${ref}" >&2
  done
done

if [[ "${DRY_RUN}" == true ]]; then
  exit 0
fi

python3 - "${INDEX_PATH}" "${entries[@]}" <<'PY'
import json
import sys
from datetime import datetime, timezone

path = sys.argv[1]
entries = [json.loads(e) for e in sys.argv[2:]]
doc = {
    "collected_at": datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
    "runs": sorted(entries, key=lambda r: (r["label"], r["arm"])),
}
with open(path, "w", encoding="utf-8") as f:
    json.dump(doc, f, indent=2)
    f.write("\n")
print(f"wrote {path} ({len(entries)} runs)")
PY

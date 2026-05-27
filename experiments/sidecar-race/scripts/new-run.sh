#!/usr/bin/env bash
# Create a new experiment run directory with metadata and empty results.csv.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/common.sh
source "${SCRIPT_DIR}/lib/common.sh"

ARM=""
NOTES=""
RUN_ID=""

usage() {
  cat <<EOF
Usage: new-run.sh --arm <sidecar|ci> [--run-id <id>] [--notes <text>]

Creates experiments/sidecar-race/results/<run-id>/ with run.json and results.csv.

Environment:
  RUN_ID   Override auto-generated run id (default: UTC timestamp)

Example:
  ./scripts/new-run.sh --arm sidecar --notes "pilot run 001"
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --arm) ARM="$2"; shift 2 ;;
    --run-id) RUN_ID="$2"; shift 2 ;;
    --notes) NOTES="$2"; shift 2 ;;
    -h | --help) usage; exit 0 ;;
    *) die "unknown argument: $1" ;;
  esac
done

[[ -n "${ARM}" ]] || die "--arm is required (sidecar or ci)"
[[ "${ARM}" == "sidecar" || "${ARM}" == "ci" ]] || die "--arm must be sidecar or ci"

if [[ -z "${RUN_ID}" ]]; then
  RUN_ID="$(date -u +"%Y%m%d-%H%M%S")"
fi

RUN_DIR="${EXPERIMENT_ROOT}/results/${RUN_ID}"
[[ ! -e "${RUN_DIR}" ]] || die "run directory already exists: ${RUN_DIR}"

mkdir -p "${RUN_DIR}"

BRANCH="$(git -C "${REPO_ROOT}" branch --show-current 2>/dev/null || echo "")"
SHA="$(git -C "${REPO_ROOT}" rev-parse HEAD 2>/dev/null || echo "")"

SNAPSHOT_ID=""
if [[ -f "${REPO_ROOT}/.chunk/config.json" ]]; then
  SNAPSHOT_ID="$(python3 -c "
import json
from pathlib import Path
p = Path('${REPO_ROOT}') / '.chunk' / 'config.json'
if p.exists():
    d = json.loads(p.read_text())
    print(d.get('validation', {}).get('sidecarImage', '') or '')
" 2>/dev/null || true)"
fi

export RUN_META_NOTES="${NOTES}"
python3 -c "
import json, os
from datetime import datetime, timezone

meta = {
    'run_id': '${RUN_ID}',
    'arm': '${ARM}',
    'created_at': datetime.now(timezone.utc).strftime('%Y-%m-%dT%H:%M:%SZ'),
    'branch': '${BRANCH}',
    'git_sha': '${SHA}',
    'sidecar_snapshot_id': '${SNAPSHOT_ID}',
    'notes': os.environ.get('RUN_META_NOTES', ''),
    'gate_jobs': ['lint', 'test'],
    'sidecar_validate': ['lint', 'test-changed'],
    'sidecar_remote': True,
}
with open('${RUN_DIR}/run.json', 'w') as f:
    json.dump(meta, f, indent=2)
    f.write('\n')
"

echo "${CSV_HEADER}" >"${RUN_DIR}/results.csv"

echo "Created run: ${RUN_ID}"
echo "  directory: ${RUN_DIR}"
echo "  arm:       ${ARM}"
echo ""
echo "Export for iteration scripts:"
echo "  export RUN_ID=${RUN_ID}"
echo ""
echo "Next steps:"
echo "  ./scripts/apply-task.sh 1"
if [[ "${ARM}" == "sidecar" ]]; then
  echo "  ./scripts/sidecar-iter.sh 1"
else
  echo "  git commit && git push   # then"
  echo "  ./scripts/ci-iter.sh 1"
fi

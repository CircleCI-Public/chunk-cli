#!/usr/bin/env bash
# Run Claude Agent SDK for one task-bank iteration (replaces apply-task.sh in run-arm).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/common.sh
source "${SCRIPT_DIR}/lib/common.sh"

ITER=""

usage() {
  cat <<EOF
Usage: run-agent-task.sh <iter>

Runs the agent_prompt for iteration <iter> from task-bank/manifest.json using
Claude Agent SDK (local edits in REPO_ROOT). Records tokens to:
  results/<run-id>/agent_usage.jsonl
  results/<run-id>/llm_usage.json

Requires: uv, ANTHROPIC_API_KEY, RUN_ID or RUN_DIR
Optional: SIDECAR_RACE_AGENT_MODEL (overrides manifest agent_model)
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    -h | --help) usage; exit 0 ;;
    *)
      if [[ -z "${ITER}" ]]; then
        ITER="$1"
        shift
      else
        die "unknown argument: $1"
      fi
      ;;
  esac
done

[[ -n "${ITER}" ]] || die "iteration number required"

if [[ -z "${ANTHROPIC_API_KEY:-}" ]]; then
  die "ANTHROPIC_API_KEY is required (chunk auth set anthropic or export the key)"
fi

require_cmd uv
require_cmd git

RUN_DIR="$(resolve_run_dir)"
export REPO_ROOT RUN_DIR

MANIFEST="${EXPERIMENT_ROOT}/task-bank/manifest.json"
[[ -f "${MANIFEST}" ]] || die "missing manifest: ${MANIFEST}"

if ! python3 -c "
import json
from pathlib import Path
m = json.loads(Path('${MANIFEST}').read_text())
tasks = {t['id']: t for t in m.get('tasks', [])}
t = tasks.get(${ITER})
if not t or not t.get('agent_prompt'):
    raise SystemExit(1)
" 2>/dev/null; then
  die "task ${ITER} has no agent_prompt in manifest.json"
fi

echo "Agent task ${ITER} (repo=${REPO_ROOT})"
uv run --project "${EXPERIMENT_ROOT}" python "${SCRIPT_DIR}/lib/agent_task.py" "${ITER}"

echo "Working tree after agent:"
git -C "${REPO_ROOT}" status --short

#!/usr/bin/env bash
# Compare sidecar-race replicates: sidecar vs CI arms (median TTS, costs, LLM).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/common.sh
source "${SCRIPT_DIR}/lib/common.sh"

LABELS=""
FROM_GIT=false
OUTPUT=""

usage() {
  cat <<EOF
Usage: compare-runs.sh [options]

Roll up per-run metrics across experiment replicates (e.g. 001–005).

Options:
  --labels <list>   Comma-separated labels (default: all discovered)
  --from-git        Read results from origin run branches (recommended when
                    results/ is gitignored locally)
  --output <path>   Write markdown report (also printed to stdout)

Examples:
  ./scripts/compare-runs.sh --from-git --labels 001,002,003,004,005
  ./scripts/compare-runs.sh --from-git -o comparison.md
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --labels) LABELS="$2"; shift 2 ;;
    --from-git) FROM_GIT=true; shift ;;
    --output) OUTPUT="$2"; shift 2 ;;
    -h | --help) usage; exit 0 ;;
    *) die "unknown argument: $1" ;;
  esac
done

ARGS=()
[[ -n "${LABELS}" ]] && ARGS+=(--labels "${LABELS}")
[[ "${FROM_GIT}" == true ]] && ARGS+=(--from-git)
[[ -n "${OUTPUT}" ]] && ARGS+=(-o "${OUTPUT}")
ARGS+=(--repo-root "${REPO_ROOT}")

export PYTHONPATH="${SCRIPT_DIR}/lib:${PYTHONPATH:-}"
python3 "${SCRIPT_DIR}/lib/compare_runs.py" "${ARGS[@]}"

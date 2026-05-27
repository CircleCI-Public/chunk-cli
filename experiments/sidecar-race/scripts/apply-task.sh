#!/usr/bin/env bash
# Apply a task-bank patch for the given iteration number.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/common.sh
source "${SCRIPT_DIR}/lib/common.sh"

ITER=""
RESET=false

usage() {
  cat <<EOF
Usage: apply-task.sh <iter> [--reset]

Applies task-bank/<patch> from manifest.json for iteration <iter>.

  --reset   Hard reset repo to base_ref in manifest before applying (destructive)

Requires: python3, git
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --reset) RESET=true; shift ;;
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

MANIFEST="${EXPERIMENT_ROOT}/task-bank/manifest.json"
[[ -f "${MANIFEST}" ]] || die "missing manifest: ${MANIFEST}"

read -r PATCH BASE_REF < <(python3 -c "
import json
from pathlib import Path

m = json.loads(Path('${MANIFEST}').read_text())
base = m.get('base_ref', 'main')
patch = ''
for t in m.get('tasks', []):
    if t.get('id') == ${ITER}:
        patch = t.get('patch') or ''
        break
else:
    raise SystemExit('no task with id ${ITER}')
print(patch, base)
")

if [[ -z "${PATCH}" ]]; then
  die "task ${ITER} has no patch file yet (patch is null in manifest.json)"
fi

PATCH_PATH="${EXPERIMENT_ROOT}/task-bank/${PATCH}"
[[ -f "${PATCH_PATH}" ]] || die "patch not found: ${PATCH_PATH}"

if ${RESET}; then
  echo "Resetting ${REPO_ROOT} to ${BASE_REF}..."
  git -C "${REPO_ROOT}" fetch origin "${BASE_REF}" 2>/dev/null || true
  git -C "${REPO_ROOT}" checkout "${BASE_REF}"
  git -C "${REPO_ROOT}" reset --hard "origin/${BASE_REF}" 2>/dev/null || git -C "${REPO_ROOT}" reset --hard "${BASE_REF}"
fi

echo "Applying ${PATCH_PATH}..."
git -C "${REPO_ROOT}" apply --whitespace=fix "${PATCH_PATH}"
echo "Applied task ${ITER}. Working tree:"
git -C "${REPO_ROOT}" status --short

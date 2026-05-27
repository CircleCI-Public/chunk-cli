#!/usr/bin/env bash
# Verify prerequisites before an experiment run.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/common.sh
source "${SCRIPT_DIR}/lib/common.sh"

ARM=""

usage() {
  cat <<EOF
Usage: prep-check.sh [--arm sidecar|ci]

Exits 0 when ready to run. Prints what is missing otherwise.

  --arm sidecar   Also require snapshot in .chunk/config.json and optional active sidecar
  --arm ci        Also require CIRCLE_TOKEN
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --arm)
      ARM="$2"
      shift 2
      ;;
    -h | --help)
      usage
      exit 0
      ;;
    *)
      die "unknown argument: $1"
      ;;
  esac
done

ok=true
say_ok() { echo "  ok: $*"; }
say_fail() { echo "  MISSING: $*" >&2; ok=false; }

echo "Sidecar race experiment — prep check"
echo "  repo:        ${REPO_ROOT}"
echo "  experiment:  ${EXPERIMENT_ROOT}"
echo "  git branch:  $(git -C "${REPO_ROOT}" branch --show-current 2>/dev/null || echo '?')"

for cmd in git python3 chunk task uv; do
  if command -v "${cmd}" >/dev/null 2>&1; then
    say_ok "${cmd} on PATH"
  else
    say_fail "${cmd} on PATH"
  fi
done

if [[ -n "${ANTHROPIC_API_KEY:-}" ]]; then
  say_ok "ANTHROPIC_API_KEY set (Claude Agent SDK)"
else
  say_fail "ANTHROPIC_API_KEY (export or: chunk auth set anthropic)"
fi

if command -v uv >/dev/null 2>&1; then
  if uv sync --project "${EXPERIMENT_ROOT}" >/dev/null 2>&1; then
    say_ok "agent SDK deps (uv sync in experiments/sidecar-race)"
  else
    say_fail "uv sync --project experiments/sidecar-race"
  fi
fi

if command -v chunk >/dev/null 2>&1; then
  if chunk auth status >/dev/null 2>&1; then
    say_ok "chunk auth"
  else
    say_fail "chunk auth (run: chunk auth set circleci)"
  fi
else
  say_fail "chunk on PATH"
fi

if [[ -f "${REPO_ROOT}/.chunk/config.json" ]]; then
  say_ok ".chunk/config.json present"
  for cmd_name in lint test-changed; do
    if python3 -c "
import json, sys
from pathlib import Path
d = json.loads(Path('${REPO_ROOT}/.chunk/config.json').read_text())
names = {c.get('name') for c in d.get('commands', [])}
sys.exit(0 if '${cmd_name}' in names else 1)
" 2>/dev/null; then
      say_ok "validate command: ${cmd_name}"
    else
      say_fail "validate command '${cmd_name}' in .chunk/config.json (chunk init / validate --save)"
    fi
  done
else
  say_fail ".chunk/config.json (run chunk init in repo root)"
fi

if [[ -f "${EXPERIMENT_ROOT}/task-bank/manifest.json" ]]; then
  task_count="$(python3 -c "import json; print(len(json.load(open('${EXPERIMENT_ROOT}/task-bank/manifest.json'))['tasks']))")"
  say_ok "task bank (${task_count} tasks)"
else
  say_fail "task-bank/manifest.json"
fi

patch_count="$(find "${EXPERIMENT_ROOT}/task-bank" -name '*.patch' | wc -l | tr -d ' ')"
if [[ "${patch_count}" -ge 10 ]]; then
  say_ok "task patches (${patch_count})"
else
  say_fail "task patches (found ${patch_count}, need 10)"
fi

branch="$(git -C "${REPO_ROOT}" branch --show-current 2>/dev/null || true)"
if [[ "${branch}" == "experiment/sidecar-race" || "${branch}" == "main" ]]; then
  echo "  note: create a run branch before run-arm.sh (e.g. $(run_branch_example sidecar))"
elif is_run_branch "${branch}"; then
  say_ok "on run branch (${branch})"
else
  echo "  note: branch is '${branch}' — expected experiment/sidecar-race--run-<id>-<arm>"
fi

if [[ "${ARM}" == "sidecar" ]]; then
  snapshot="$(python3 -c "
import json
from pathlib import Path
p = Path('${REPO_ROOT}') / '.chunk' / 'config.json'
if p.exists():
    d = json.loads(p.read_text())
    print(d.get('validation', {}).get('sidecarImage', '') or '')
" 2>/dev/null || true)"
  if [[ -n "${snapshot}" ]]; then
    say_ok "validation.sidecarImage configured"
  else
    say_fail "validation.sidecarImage in .chunk/config.json (snapshot after chunk sidecar setup)"
  fi
  if has_active_sidecar; then
    say_ok "active sidecar"
  else
    echo "  note: no active sidecar — run-arm.sh will call ensure-sidecar.sh first"
  fi
  if [[ -n "${CIRCLE_TOKEN:-${CIRCLECI_TOKEN:-}}" ]]; then
    say_ok "CIRCLE_TOKEN set (required for epilogue)"
  else
    say_fail "CIRCLE_TOKEN (required for sidecar epilogue CI validation)"
  fi
fi

if [[ "${ARM}" == "ci" ]]; then
  if [[ -n "${CIRCLE_TOKEN:-${CIRCLECI_TOKEN:-}}" ]]; then
    say_ok "CIRCLE_TOKEN set"
  else
    say_fail "CIRCLE_TOKEN (required for ci arm)"
  fi
fi

if [[ "${ok}" == true ]]; then
  echo ""
  echo "Ready for --arm ${ARM:-<pick sidecar or ci>}."
  exit 0
fi

echo ""
echo "Fix the items above, then re-run prep-check.sh --arm ${ARM:-sidecar}"
exit 1

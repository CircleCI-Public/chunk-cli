#!/usr/bin/env bash
# Create or update a PR for a run branch with a metrics summary in the body.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/common.sh
source "${SCRIPT_DIR}/lib/common.sh"

RUN_LABEL=""
ARM=""
BOOTSTRAP=false
UPDATE=false
COMMIT_RESULTS=false
BASE_BRANCH="${EXPERIMENT_BASE_BRANCH:-experiment/sidecar-race}"
HARNESS_PR_URL="${HARNESS_PR_URL:-https://github.com/CircleCI-Public/chunk-cli/pull/370}"
DRAFT=true
MARK_READY=true

usage() {
  cat <<EOF
Usage: open-run-pr.sh --run-id <label> --arm <sidecar|ci> [--bootstrap | --update] [options]

  --run-id <label>  Run label in branch name (e.g. 001 → experiment/sidecar-race--run-001-sidecar)
  --bootstrap       Create run branch, push, open **draft** PR (pre-run)
  --update          Refresh PR body from results; mark **ready for review** when run completed
  --commit-results  git add -f results/<RUN_ID>/ and push before updating PR
  --no-mark-ready   Leave PR as draft after --update (default: mark ready when results exist)

Environment:
  RUN_ID              Results directory id (timestamp from new-run.sh); required for --update
  EXPERIMENT_BASE_BRANCH   Base branch (default: experiment/sidecar-race)

Example (before running):
  ./scripts/open-run-pr.sh --run-id 001 --arm sidecar --bootstrap
  ./scripts/open-run-pr.sh --run-id 001 --arm ci --bootstrap

After run-arm.sh:
  export RUN_ID=20260527-120000
  ./scripts/open-run-pr.sh --run-id 001 --arm sidecar --update --commit-results
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --run-id | --label) RUN_LABEL="$2"; shift 2 ;;
    --arm) ARM="$2"; shift 2 ;;
    --bootstrap) BOOTSTRAP=true; shift ;;
    --update) UPDATE=true; shift ;;
    --commit-results) COMMIT_RESULTS=true; shift ;;
    --base) BASE_BRANCH="$2"; shift 2 ;;
    --no-draft) DRAFT=false; shift ;;
    --no-mark-ready) MARK_READY=false; shift ;;
    -h | --help) usage; exit 0 ;;
    *) die "unknown argument: $1" ;;
  esac
done

[[ -n "${RUN_LABEL}" ]] || die "--run-id <label> is required (e.g. 001)"
[[ -n "${ARM}" ]] || die "--arm is required (sidecar or ci)"
[[ "${ARM}" == "sidecar" || "${ARM}" == "ci" ]] || die "--arm must be sidecar or ci"
[[ "${BOOTSTRAP}" == true || "${UPDATE}" == true ]] || die "use --bootstrap or --update"

RUN_BRANCH="$(run_branch_example "${ARM}" "${RUN_LABEL}")"
TITLE="experiment: sidecar race run ${RUN_LABEL} (${ARM})"

run_completed() {
  local run_dir="$1"
  [[ -f "${run_dir}/results.csv" ]] || return 1
  python3 - "${run_dir}/results.csv" <<'PY'
import csv, sys
rows = list(csv.DictReader(open(sys.argv[1])))
iters = [r for r in rows if str(r.get("iter", "")).isdigit()]
sys.exit(0 if len(iters) >= 1 else 1)
PY
}

pr_body_file() {
  local run_dir="${1:-}"
  local tmp
  tmp="$(mktemp)"
  if [[ -n "${run_dir}" && -d "${run_dir}" ]]; then
    python3 "${SCRIPT_DIR}/lib/render_run_pr_body.py" "${run_dir}" "${HARNESS_PR_URL}" >"${tmp}"
  else
    local bootstrap_dir="${EXPERIMENT_ROOT}/results/.bootstrap-${RUN_LABEL}-${ARM}"
    mkdir -p "${bootstrap_dir}"
    python3 -c "
import json
from pathlib import Path
p = Path('${bootstrap_dir}/run.json')
p.write_text(json.dumps({
    'run_id': '${RUN_LABEL}',
    'arm': '${ARM}',
    'branch': '${RUN_BRANCH}',
    'notes': 'bootstrap PR — metrics pending',
}, indent=2) + '\n')
"
    python3 "${SCRIPT_DIR}/lib/render_run_pr_body.py" "${bootstrap_dir}" "${HARNESS_PR_URL}" >"${tmp}"
  fi
  echo "${tmp}"
}

ensure_branch() {
  git -C "${REPO_ROOT}" fetch origin "${BASE_BRANCH}" 2>/dev/null || true
  if git -C "${REPO_ROOT}" show-ref --verify --quiet "refs/heads/${RUN_BRANCH}"; then
    git -C "${REPO_ROOT}" checkout "${RUN_BRANCH}"
  else
    git -C "${REPO_ROOT}" checkout -B "${RUN_BRANCH}" "origin/${BASE_BRANCH}" 2>/dev/null \
      || git -C "${REPO_ROOT}" checkout -B "${RUN_BRANCH}" "${BASE_BRANCH}"
  fi
  git -C "${REPO_ROOT}" push -u origin "${RUN_BRANCH}"
  # GitHub requires at least one commit difference from base for a PR.
  if git -C "${REPO_ROOT}" rev-parse "${RUN_BRANCH}" \
    | grep -q "$(git -C "${REPO_ROOT}" rev-parse "origin/${BASE_BRANCH}" 2>/dev/null || git -C "${REPO_ROOT}" rev-parse "${BASE_BRANCH}")"; then
    git -C "${REPO_ROOT}" commit --allow-empty -m "experiment: begin run ${RUN_LABEL} (${ARM})"
    git -C "${REPO_ROOT}" push origin "${RUN_BRANCH}"
  fi
}

if [[ "${BOOTSTRAP}" == true ]]; then
  require_cmd gh
  ensure_branch
  body="$(pr_body_file "")"
  trap 'rm -f "${body}"' EXIT
  existing="$(gh pr list --head "${RUN_BRANCH}" --base "${BASE_BRANCH}" --json number --jq '.[0].number' 2>/dev/null || true)"
  if [[ -n "${existing}" && "${existing}" != "null" ]]; then
    gh pr edit "${existing}" --title "${TITLE}" --body-file "${body}"
    echo "Updated existing draft PR #${existing} for ${RUN_BRANCH}"
    gh pr view "${existing}" --web 2>/dev/null || gh pr view "${existing}"
  else
    args=(pr create --base "${BASE_BRANCH}" --head "${RUN_BRANCH}" --title "${TITLE}" --body-file "${body}" --draft)
    gh "${args[@]}"
  fi
  exit 0
fi

if [[ "${UPDATE}" == true ]]; then
  require_cmd gh
  : "${RUN_ID:?RUN_ID is required for --update (timestamp from new-run.sh / run-arm.sh)}"

  RUN_DIR="$(resolve_run_dir)"
  branch="$(git -C "${REPO_ROOT}" branch --show-current)"
  [[ "${branch}" == "${RUN_BRANCH}" ]] || die "checkout ${RUN_BRANCH} (on ${branch})"

  if [[ "${COMMIT_RESULTS}" == true ]]; then
    "${SCRIPT_DIR}/finalize-metrics.sh" 2>/dev/null || true
    "${SCRIPT_DIR}/summarize-run.sh" 2>/dev/null || true
    git -C "${REPO_ROOT}" add -f "${RUN_DIR}/"
    if ! git -C "${REPO_ROOT}" diff --cached --quiet; then
      git -C "${REPO_ROOT}" commit -m "experiment: results ${RUN_ID} (${ARM})"
      git -C "${REPO_ROOT}" push origin "${RUN_BRANCH}"
    fi
  fi

  body="$(pr_body_file "${RUN_DIR}")"
  trap 'rm -f "${body}"' EXIT
  pr_num="$(gh pr list --head "${RUN_BRANCH}" --base "${BASE_BRANCH}" --json number --jq '.[0].number')"
  [[ -n "${pr_num}" && "${pr_num}" != "null" ]] || die "no PR found for ${RUN_BRANCH} → ${BASE_BRANCH}; run --bootstrap first"
  gh pr edit "${pr_num}" --title "${TITLE}" --body-file "${body}"

  if [[ "${MARK_READY}" == true ]] && run_completed "${RUN_DIR}"; then
    if gh pr ready "${pr_num}" 2>/dev/null; then
      echo "Marked PR #${pr_num} ready for review"
    else
      echo "PR #${pr_num} body updated (already ready or could not mark ready)"
    fi
  else
    echo "PR #${pr_num} body updated (still draft — run incomplete or --no-mark-ready)"
  fi
  gh pr view "${pr_num}"
fi

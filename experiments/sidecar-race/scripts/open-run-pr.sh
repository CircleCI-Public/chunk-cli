#!/usr/bin/env bash
# Open or update a run PR with metrics in the body (draft until the run completes).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/common.sh
source "${SCRIPT_DIR}/lib/common.sh"

RUN_LABEL=""
ARM=""
ENSURE_DRAFT=false
UPDATE=false
COMMIT_RESULTS=false
BASE_BRANCH="${EXPERIMENT_BASE_BRANCH:-experiment/sidecar-race}"
HARNESS_PR_URL="${HARNESS_PR_URL:-https://github.com/CircleCI-Public/chunk-cli/pull/370}"
DRAFT=true
MARK_READY=true

usage() {
  cat <<EOF
Usage: open-run-pr.sh --run-id <label> --arm <sidecar|ci> [--ensure-draft | --update] [options]

  --ensure-draft    Open a draft PR only if the run branch has ≥1 commit ahead of ${BASE_BRANCH}
                    and no PR exists yet (called automatically after first push/commit).
  --update          Refresh PR body from results; mark ready for review when the run completed
  --commit-results  git add -f results/<RUN_ID>/ and push before --update

Environment:
  RUN_ID            Results directory id (required for --update)

Example:
  # No manual step before run-arm — draft PR opens on first commit (CI task 1 or sidecar epilogue).
  ./scripts/run-arm.sh --arm sidecar --notes "run 001"
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --run-id | --label) RUN_LABEL="$2"; shift 2 ;;
    --arm) ARM="$2"; shift 2 ;;
    --ensure-draft) ENSURE_DRAFT=true; shift ;;
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
[[ "${ENSURE_DRAFT}" == true || "${UPDATE}" == true ]] || die "use --ensure-draft or --update"

RUN_BRANCH="$(run_branch_example "${ARM}" "${RUN_LABEL}")"
TITLE="experiment: sidecar race run ${RUN_LABEL} (${ARM})"

commits_ahead_of_base() {
  git -C "${REPO_ROOT}" fetch origin "${BASE_BRANCH}" 2>/dev/null || true
  local base_sha
  base_sha="$(git -C "${REPO_ROOT}" rev-parse "origin/${BASE_BRANCH}" 2>/dev/null \
    || git -C "${REPO_ROOT}" rev-parse "${BASE_BRANCH}")"
  git -C "${REPO_ROOT}" rev-list --count "${base_sha}..HEAD" 2>/dev/null || echo "0"
}

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
  if [[ -n "${run_dir}" && -d "${run_dir}" && -f "${run_dir}/run.json" ]]; then
    python3 "${SCRIPT_DIR}/lib/render_run_pr_body.py" "${run_dir}" "${HARNESS_PR_URL}" >"${tmp}"
  else
    die "cannot render PR body: no results run directory (set RUN_ID)"
  fi
  echo "${tmp}"
}

existing_pr_num() {
  gh pr list --head "${RUN_BRANCH}" --base "${BASE_BRANCH}" --json number --jq '.[0].number' 2>/dev/null || true
}

if [[ "${ENSURE_DRAFT}" == true ]]; then
  require_cmd gh
  branch="$(git -C "${REPO_ROOT}" branch --show-current)"
  [[ "${branch}" == "${RUN_BRANCH}" ]] || die "checkout ${RUN_BRANCH} (on ${branch})"

  ahead="$(commits_ahead_of_base)"
  if [[ "${ahead}" -lt 1 ]]; then
    echo "No draft PR: ${RUN_BRANCH} has no commits ahead of ${BASE_BRANCH} yet."
    exit 0
  fi

  pr_num="$(existing_pr_num)"
  if [[ -n "${pr_num}" && "${pr_num}" != "null" ]]; then
    echo "Draft PR already exists: #${pr_num}"
    exit 0
  fi

  run_dir=""
  if [[ -n "${RUN_ID:-}" ]]; then
    run_dir="$(resolve_run_dir 2>/dev/null)" || true
  fi
  [[ -n "${run_dir}" && -f "${run_dir}/run.json" ]] || die "RUN_ID must be set and run.json must exist before opening draft PR"

  body="$(pr_body_file "${run_dir}")"
  trap 'rm -f "${body}"' EXIT
  args=(pr create --base "${BASE_BRANCH}" --head "${RUN_BRANCH}" --title "${TITLE}" --body-file "${body}")
  [[ "${DRAFT}" == true ]] && args+=(--draft)
  gh "${args[@]}"
  exit 0
fi

if [[ "${UPDATE}" == true ]]; then
  require_cmd gh
  : "${RUN_ID:?RUN_ID is required for --update}"

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

  ahead="$(commits_ahead_of_base)"
  [[ "${ahead}" -ge 1 ]] || die "no commits on ${RUN_BRANCH} ahead of ${BASE_BRANCH}; nothing to publish"

  pr_num="$(existing_pr_num)"
  if [[ -z "${pr_num}" || "${pr_num}" == "null" ]]; then
    export RUN_ID
    "${SCRIPT_DIR}/open-run-pr.sh" --run-id "${RUN_LABEL}" --arm "${ARM}" --ensure-draft
    pr_num="$(existing_pr_num)"
  fi
  [[ -n "${pr_num}" && "${pr_num}" != "null" ]] || die "could not find or create PR for ${RUN_BRANCH}"

  body="$(pr_body_file "${RUN_DIR}")"
  trap 'rm -f "${body}"' EXIT
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

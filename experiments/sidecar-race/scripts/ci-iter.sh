#!/usr/bin/env bash
# Record one CI-arm iteration: push (optional) + poll lint/test jobs.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/common.sh
source "${SCRIPT_DIR}/lib/common.sh"

ITER=""
NOTES=""
SKIP_PUSH=false
BRANCH=""

usage() {
  cat <<EOF
Usage: ci-iter.sh <iter> [--notes <text>] [--skip-push]

Requires RUN_ID or RUN_DIR from new-run.sh (arm=ci).
Requires CIRCLE_TOKEN and a pushed commit on the run branch.

By default runs 'git push' from repo root before polling. Use --skip-push if
you already pushed for this iteration.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --notes) NOTES="$2"; shift 2 ;;
    --skip-push) SKIP_PUSH=true; shift ;;
    --branch) BRANCH="$2"; shift 2 ;;
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

CIRCLE_TOKEN="${CIRCLE_TOKEN:-${CIRCLECI_TOKEN:-}}"
: "${CIRCLE_TOKEN:?CIRCLE_TOKEN is required}"

init_csv_if_missing

ARM="$(read_run_field arm)"
[[ "${ARM}" == "ci" ]] || die "run arm is '${ARM}', expected ci"

RUN_ID="$(read_run_field run_id)"
STARTED="$(iso_timestamp)"
START_EPOCH="$(epoch_seconds)"
export START_EPOCH

if [[ -z "${BRANCH}" ]]; then
  BRANCH="$(git -C "${REPO_ROOT}" branch --show-current)"
fi

if [[ "${SKIP_PUSH}" != true ]]; then
  echo "Pushing ${REPO_ROOT} (branch ${BRANCH})..."
  git -C "${REPO_ROOT}" push -u origin "${BRANCH}"
  sleep 5
fi

echo "Polling CircleCI gate jobs (lint, test)..."
CSV_LINE="$("${SCRIPT_DIR}/poll-ci-gate.sh" --branch "${BRANCH}")"

IFS=',' read -r PIPELINE_ID WORKFLOW_ID LINT_JOB TEST_JOB LINT_OK TEST_OK LINT_DUR TEST_DUR TTS <<<"${CSV_LINE}"

ENDED="$(iso_timestamp)"
SHA="$(git_short_sha)"

append_csv_row \
  "ci,${RUN_ID},${ITER},${STARTED},${ENDED},${TTS},${LINT_OK},${TEST_OK},${LINT_DUR},${TEST_DUR},,${PIPELINE_ID},${WORKFLOW_ID},${LINT_JOB},${TEST_JOB},${SHA},${NOTES}"

echo "Recorded CI iteration ${ITER}: tts=${TTS}s lint=${LINT_OK} test=${TEST_OK}"

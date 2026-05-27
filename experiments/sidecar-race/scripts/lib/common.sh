#!/usr/bin/env bash
# Shared helpers for sidecar-race experiment scripts.
set -euo pipefail

EXPERIMENT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
REPO_ROOT="$(cd "${EXPERIMENT_ROOT}/../.." && pwd)"

export EXPERIMENT_ROOT REPO_ROOT

CSV_HEADER="arm,run_id,iter,started_at,ended_at,tts_seconds,lint_ok,test_ok,lint_duration_s,test_duration_s,sync_duration_s,ci_pipeline_id,ci_workflow_id,ci_lint_job_num,ci_test_job_num,git_sha,notes"

die() {
  echo "error: $*" >&2
  exit 1
}

require_cmd() {
  local cmd="$1"
  command -v "${cmd}" >/dev/null 2>&1 || die "${cmd} not found on PATH"
}

iso_timestamp() {
  date -u +"%Y-%m-%dT%H:%M:%SZ"
}

epoch_seconds() {
  date +%s
}

resolve_run_dir() {
  if [[ -n "${RUN_DIR:-}" && -d "${RUN_DIR}" ]]; then
    echo "${RUN_DIR}"
    return
  fi
  if [[ -n "${RUN_ID:-}" ]]; then
    local dir="${EXPERIMENT_ROOT}/results/${RUN_ID}"
    [[ -d "${dir}" ]] || die "RUN_ID=${RUN_ID} but ${dir} does not exist (run new-run.sh first)"
    echo "${dir}"
    return
  fi
  local latest
  latest="$(find "${EXPERIMENT_ROOT}/results" -mindepth 1 -maxdepth 1 -type d 2>/dev/null | sort | tail -1)"
  [[ -n "${latest}" ]] || die "no run directory found; set RUN_ID or RUN_DIR or run new-run.sh"
  echo "${latest}"
}

run_csv() {
  local run_dir
  run_dir="$(resolve_run_dir)"
  echo "${run_dir}/results.csv"
}

run_json() {
  local run_dir
  run_dir="$(resolve_run_dir)"
  echo "${run_dir}/run.json"
}

read_run_field() {
  local field="$1"
  python3 -c "
import json, sys
with open(sys.argv[1]) as f:
    d = json.load(f)
print(d.get(sys.argv[2], ''))
" "$(run_json)" "${field}"
}

append_csv_row() {
  local csv
  csv="$(run_csv)"
  echo "$*" >>"${csv}"
}

init_csv_if_missing() {
  local csv
  csv="$(run_csv)"
  if [[ ! -f "${csv}" ]]; then
    echo "${CSV_HEADER}" >"${csv}"
  fi
}

bool_from_exit() {
  if [[ "$1" -eq 0 ]]; then
    echo "pass"
  else
    echo "fail"
  fi
}

git_short_sha() {
  git -C "${REPO_ROOT}" rev-parse --short HEAD 2>/dev/null || echo ""
}

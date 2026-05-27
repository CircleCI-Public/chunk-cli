#!/usr/bin/env bash
# Record one sidecar-arm iteration: sync + remote validate lint + test-changed.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/common.sh
source "${SCRIPT_DIR}/lib/common.sh"

ITER=""
NOTES=""

usage() {
  cat <<EOF
Usage: sidecar-iter.sh <iter> [--notes <text>]

Requires RUN_ID or RUN_DIR from new-run.sh (arm=sidecar).
Requires active sidecar (chunk sidecar current).

Runs gates on the sidecar via: chunk validate --remote lint|test-changed
Does not commit changes. Run run-agent-task.sh first (or apply-task.sh with --replay-patches).
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --notes) NOTES="$2"; shift 2 ;;
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

require_cmd chunk
init_csv_if_missing

ARM="$(read_run_field arm)"
[[ "${ARM}" == "sidecar" ]] || die "run arm is '${ARM}', expected sidecar"

RUN_ID="$(read_run_field run_id)"
STARTED="$(iso_timestamp)"
START_EPOCH="$(epoch_seconds)"

SYNC_START="$(epoch_seconds)"
set +e
chunk_in_repo sidecar sync 2>&1
SYNC_EXIT=$?
set -e
SYNC_END="$(epoch_seconds)"
SYNC_DURATION=$((SYNC_END - SYNC_START))
[[ "${SYNC_EXIT}" -eq 0 ]] || die "chunk sidecar sync failed (exit ${SYNC_EXIT})"

LINT_START="$(epoch_seconds)"
set +e
chunk_in_repo validate --remote lint 2>&1
LINT_EXIT=$?
set -e
LINT_END="$(epoch_seconds)"
LINT_DURATION=$((LINT_END - LINT_START))

TEST_START="$(epoch_seconds)"
set +e
chunk_in_repo validate --remote test-changed 2>&1
TEST_EXIT=$?
set -e
TEST_END="$(epoch_seconds)"
TEST_DURATION=$((TEST_END - TEST_START))

ENDED="$(iso_timestamp)"
END_EPOCH="$(epoch_seconds)"
TTS=$((END_EPOCH - START_EPOCH))

SHA="$(git_short_sha)"
LINT_OK="$(bool_from_exit "${LINT_EXIT}")"
TEST_OK="$(bool_from_exit "${TEST_EXIT}")"

append_csv_row \
  "sidecar,${RUN_ID},${ITER},${STARTED},${ENDED},${TTS},${LINT_OK},${TEST_OK},${LINT_DURATION},${TEST_DURATION},${SYNC_DURATION},,,,,${SHA},${NOTES},,,,,,"

RUN_DIR="$(resolve_run_dir)"
python3 - "${RUN_DIR}" <<PY
import sys
from pathlib import Path
sys.path.insert(0, "${SCRIPT_DIR}/lib")
from log_metrics import append_event
append_event(Path(sys.argv[1]), {
    "kind": "sidecar_iter",
    "iter": ${ITER},
    "tts_seconds": ${TTS},
    "lint_duration_s": ${LINT_DURATION},
    "test_duration_s": ${TEST_DURATION},
    "sync_duration_s": ${SYNC_DURATION},
})
PY

echo "Recorded sidecar iteration ${ITER}: tts=${TTS}s lint=${LINT_OK} test=${TEST_OK} sync=${SYNC_DURATION}s"

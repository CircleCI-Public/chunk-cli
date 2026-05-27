#!/usr/bin/env bash
# Verify cumulative task 1-N tree will pass CircleCI before epilogue push.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/common.sh
source "${SCRIPT_DIR}/lib/common.sh"

TO_TASK=10

usage() {
  cat <<EOF
Usage: verify-epilogue-ready.sh [--to-task <n>]

Runs the same local gates that block the CircleCI "ci" workflow on epilogue push:
  - shellcheck on all *.sh in the repo (matches circleci/shellcheck orb)
  - task lint
  - go test -race on packages touched by the task bank

Apply tasks 1-N on the working tree before calling (sidecar-epilogue does not apply patches).
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --to-task) TO_TASK="$2"; shift 2 ;;
    -h | --help) usage; exit 0 ;;
    *) die "unknown argument: $1" ;;
  esac
done

require_cmd shellcheck
require_cmd task
require_cmd go

echo "shellcheck: all repository shell scripts..."
sh_files=()
while IFS= read -r f; do
  sh_files+=("${f}")
done < <(find "${REPO_ROOT}" -name '*.sh' -not -path '*/.git/*' | sort)
[[ ${#sh_files[@]} -gt 0 ]] || die "no shell scripts found"
shellcheck "${sh_files[@]}"

echo "task lint..."
( cd "${REPO_ROOT}" && task lint )

echo "go test -race (racefixture + config)..."
( cd "${REPO_ROOT}" && go test -race ./internal/racefixture/... ./internal/config/... )

echo "Epilogue tree OK for tasks 1-${TO_TASK} (ready to push)."

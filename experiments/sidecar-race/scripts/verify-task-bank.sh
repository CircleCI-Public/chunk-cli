#!/usr/bin/env bash
# Apply all task patches cumulatively and check lint/test against manifest expect.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/common.sh
source "${SCRIPT_DIR}/lib/common.sh"

require_cmd go
require_cmd python3

MANIFEST="${EXPERIMENT_ROOT}/task-bank/manifest.json"
BASE_REF="$(python3 -c "import json; print(json.load(open('${MANIFEST}'))['base_ref'])")"

echo "Resetting tracked tree from origin/${BASE_REF} (keeps current branch)..."
git -C "${REPO_ROOT}" fetch origin "${BASE_REF}" 2>/dev/null || true
rm -rf "${REPO_ROOT}/internal/racefixture"
git -C "${REPO_ROOT}" checkout "origin/${BASE_REF}" -- internal/config/config_test.go internal/cmd/sidecar.go 2>/dev/null || true

failures=0
while IFS= read -r line; do
  iter="$(echo "${line}" | python3 -c "import json,sys; print(json.loads(sys.stdin.read())['id'])")"
  expect_lint="$(echo "${line}" | python3 -c "import json,sys; print(json.loads(sys.stdin.read())['expect']['lint'])")"
  expect_test="$(echo "${line}" | python3 -c "import json,sys; print(json.loads(sys.stdin.read())['expect']['test'])")"

  echo "--- Task ${iter} ---"
  "${SCRIPT_DIR}/apply-task.sh" "${iter}"

  lint_pkgs=(./internal/racefixture/...)
  test_pkgs=(./internal/racefixture/...)
  if [[ "${iter}" -ge 4 ]]; then
    lint_pkgs+=(./internal/config/...)
    test_pkgs+=(./internal/config/...)
  fi

  set +e
  go tool golangci-lint run "${lint_pkgs[@]}" >/dev/null 2>&1
  lint_exit=$?
  go test -race "${test_pkgs[@]}" >/dev/null 2>&1
  test_exit=$?
  set -e

  lint_got="$(bool_from_exit "${lint_exit}")"
  test_got="$(bool_from_exit "${test_exit}")"

  if [[ "${lint_got}" != "${expect_lint}" || "${test_got}" != "${expect_test}" ]]; then
    echo "  FAIL: lint got=${lint_got} want=${expect_lint}, test got=${test_got} want=${expect_test}"
    failures=$((failures + 1))
  else
    echo "  OK: lint=${lint_got} test=${test_got}"
  fi
done < <(python3 -c "
import json
from pathlib import Path
for t in json.loads(Path('${MANIFEST}').read_text())['tasks']:
    print(json.dumps(t))
")

if [[ "${failures}" -gt 0 ]]; then
  die "${failures} task(s) did not match manifest expect"
fi

echo "Verifying cumulative tree passes epilogue CI gates..."
"${SCRIPT_DIR}/verify-epilogue-ready.sh" --to-task "$(python3 -c "import json; print(len(json.load(open('${MANIFEST}'))['tasks']))")"

echo "All tasks match manifest expect."

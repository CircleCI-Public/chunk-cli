#!/usr/bin/env bash
# Regenerate task-bank/*.patch from a linear commit series on the current branch.
# Run from repo root after resetting to the scaffolding commit (8df560f).
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
DIR="${REPO_ROOT}/experiments/sidecar-race/task-bank"
SLUGS=(
  fix-unit-test
  introduce-lint-violation
  fix-lint-violation
  two-package-change
  break-test-then-fix
  fix-broken-test
  fmt-drift
  internal-refactor
  add-test-case
  error-handling-tweak
)

cd "${REPO_ROOT}"
rm -rf internal/racefixture
python3 <<'PY'
from pathlib import Path

root = Path("internal/racefixture")
root.mkdir(parents=True)
(root / "racefixture.go").write_text(
    "package racefixture\n\n"
    "// Sum returns the sum of a and b.\n"
    "func Sum(a, b int) int {\n"
    "\treturn a + b\n"
    "}\n"
)
(root / "racefixture_test.go").write_text(
    "package racefixture\n\n"
    "import \"testing\"\n\n"
    "func TestSum(t *testing.T) {\n"
    "\tif got := Sum(1, 2); got != 3 {\n"
    "\t\tt.Fatalf(\"Sum(1, 2) = %d, want 3\", got)\n"
    "\t}\n"
    "}\n"
)
PY
git add internal/racefixture && git commit -m "task 1: fix-unit-test"

echo 'var lintTrap = 1' >> internal/racefixture/racefixture.go
git add internal/racefixture/racefixture.go && git commit -m "task 2: introduce-lint-violation"

python3 -c "
from pathlib import Path
p = Path('internal/racefixture/racefixture.go')
p.write_text(p.read_text().replace('\nvar lintTrap = 1\n', '\n'))
"
git add internal/racefixture/racefixture.go && git commit -m "task 3: fix-lint-violation"

cat > internal/racefixture/multiply.go <<'EOF'
package racefixture

// Multiply returns the product of a and b.
func Multiply(a, b int) int {
	return a * b
}
EOF
cat > internal/racefixture/multiply_test.go <<'EOF'
package racefixture

import "testing"

func TestMultiply(t *testing.T) {
	if got := Multiply(3, 4); got != 12 {
		t.Fatalf("Multiply(3, 4) = %d, want 12", got)
	}
}
EOF
python3 -c "
from pathlib import Path
p = Path('internal/config/config_test.go')
text = p.read_text()
needle = 'func setupTempConfig(t *testing.T) string {'
comment = '// setupTempConfig creates an isolated XDG config home for tests.\n'
if comment not in text:
    p.write_text(text.replace(needle, comment + needle, 1))
"
git add internal/racefixture internal/config/config_test.go && git commit -m "task 4: two-package-change"

python3 -c "
from pathlib import Path
p = Path('internal/racefixture/racefixture_test.go')
text = p.read_text()
text = text.replace('got != 3', 'got != 0').replace('want 3', 'want 0')
p.write_text(text)
"
git add internal/racefixture/racefixture_test.go && git commit -m "task 5: break-test-then-fix"

python3 -c "
from pathlib import Path
p = Path('internal/racefixture/racefixture_test.go')
text = p.read_text()
text = text.replace('got != 0', 'got != 3').replace('want 0', 'want 3')
p.write_text(text)
"
git add internal/racefixture/racefixture_test.go && git commit -m "task 6: fix-broken-test"

python3 -c "
from pathlib import Path
p = Path('internal/racefixture/racefixture.go')
p.write_text(p.read_text().replace('return a + b', 'return  a+b'))
"
git add internal/racefixture/racefixture.go && git commit -m "task 7: fmt-drift"

cat > internal/racefixture/racefixture.go <<'EOF'
package racefixture

// Sum returns the sum of a and b.
func Sum(a, b int) int {
	return addInts(a, b)
}

func addInts(a, b int) int {
	return a + b
}
EOF
git add internal/racefixture/racefixture.go && git commit -m "task 8: internal-refactor"

cat >> internal/racefixture/multiply_test.go <<'EOF'

func TestMultiplyTable(t *testing.T) {
	tests := []struct {
		a, b, want int
	}{
		{2, 3, 6},
		{0, 5, 0},
	}
	for _, tc := range tests {
		if got := Multiply(tc.a, tc.b); got != tc.want {
			t.Fatalf("Multiply(%d, %d) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}
EOF
git add internal/racefixture/multiply_test.go && git commit -m "task 9: add-test-case"

cat > internal/racefixture/errors.go <<'EOF'
package racefixture

import (
	"errors"
	"fmt"
)

// ErrNegative is returned when a value is below zero.
var ErrNegative = errors.New("negative value")

// ValidatePositive returns an error if n is negative.
func ValidatePositive(n int) error {
	if n < 0 {
		return fmt.Errorf("racefixture: %w", ErrNegative)
	}
	return nil
}
EOF
cat > internal/racefixture/errors_test.go <<'EOF'
package racefixture

import (
	"errors"
	"testing"
)

func TestValidatePositive(t *testing.T) {
	if err := ValidatePositive(-1); err == nil {
		t.Fatal("expected error for negative value")
	} else if !errors.Is(err, ErrNegative) {
		t.Fatalf("expected ErrNegative, got %v", err)
	}
	if err := ValidatePositive(0); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
EOF
git add internal/racefixture/errors.go internal/racefixture/errors_test.go && git commit -m "task 10: error-handling-tweak"

BASE="$(git rev-parse 8df560f)"
COMMITS=()
while IFS= read -r c; do
  COMMITS+=("${c}")
done < <(git rev-list --reverse "${BASE}"..HEAD)

if [[ "${#COMMITS[@]}" -ne 10 ]]; then
  echo "error: expected 10 commits, got ${#COMMITS[@]}" >&2
  exit 1
fi

for idx in "${!COMMITS[@]}"; do
  c="${COMMITS[$idx]}"
  p="$(git rev-parse "${c}^")"
  slug="${SLUGS[$idx]}"
  num=$(printf "%02d" $((idx + 1)))
  git diff "${p}" "${c}" > "${DIR}/${num}-${slug}.patch"
  echo "wrote ${num}-${slug}.patch"
done

# Drop committed fixture from branch tip; keep only patches
git reset --hard "${BASE}"
rm -rf internal/racefixture
git checkout HEAD -- internal/config/config_test.go 2>/dev/null || true

echo "Done. Patches in ${DIR}/; branch reset to scaffolding + uncommitted patches."

package mutate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

func TestMutationPatch(t *testing.T) {
	src := `package foo

func compare(a, b int) bool {
	return a < b
}
`
	dir := t.TempDir()
	file := filepath.Join(dir, "foo.go")
	assert.NilError(t, os.WriteFile(file, []byte(src), 0o600))

	m := Mutation{
		File:     "foo.go",
		Line:     4,
		Col:      11,
		Operator: "CONDITIONALS_BOUNDARY",
	}

	patch, err := MutationPatch(dir, m)
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(string(patch), "--- a/foo.go"), "expected unified diff header, got:\n%s", patch)
	assert.Assert(t, strings.Contains(string(patch), "+++ b/foo.go"), "expected unified diff header, got:\n%s", patch)
	assert.Assert(t, strings.Contains(string(patch), "-\treturn a < b"), "expected original line in patch, got:\n%s", patch)
	assert.Assert(t, strings.Contains(string(patch), "+\treturn a <= b"), "expected mutated line in patch, got:\n%s", patch)

	current, err := os.ReadFile(file)
	assert.NilError(t, err)
	assert.Equal(t, string(current), src)
}

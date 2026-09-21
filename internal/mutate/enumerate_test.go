package mutate

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/envspec"
)

func TestGoEnumerator_findsOperators(t *testing.T) {
	src := `package foo

func compare(a, b int) bool {
	return a < b
}
`
	dir := t.TempDir()
	assert.NilError(t, os.WriteFile(filepath.Join(dir, "foo.go"), []byte(src), 0o600))

	e := &GoEnumerator{}
	mutations, err := e.Enumerate(context.Background(), dir)
	assert.NilError(t, err)

	// < matches CONDITIONALS_BOUNDARY and CONDITIONALS_NEGATION
	assert.Assert(t, len(mutations) >= 2, "expected at least 2 mutations, got %d", len(mutations))

	ops := map[string]bool{}
	for _, m := range mutations {
		ops[m.Operator] = true
		assert.Equal(t, m.Status, "RUNNABLE")
		assert.Equal(t, m.File, "foo.go")
		assert.Equal(t, m.Line, 4)
	}
	assert.Assert(t, ops[OpConditionalsBoundary], "expected CONDITIONALS_BOUNDARY")
	assert.Assert(t, ops[OpConditionalsNegation], "expected CONDITIONALS_NEGATION")
}

func TestGoEnumerator_skipsTestFiles(t *testing.T) {
	src := `package foo
func f(a, b int) bool { return a < b }
`
	dir := t.TempDir()
	assert.NilError(t, os.WriteFile(filepath.Join(dir, "foo_test.go"), []byte(src), 0o600))

	e := &GoEnumerator{}
	mutations, err := e.Enumerate(context.Background(), dir)
	assert.NilError(t, err)
	assert.Equal(t, len(mutations), 0)
}

func TestGoEnumerator_skipsVendor(t *testing.T) {
	src := `package foo
func f(a, b int) bool { return a < b }
`
	dir := t.TempDir()
	vendor := filepath.Join(dir, "vendor", "pkg")
	assert.NilError(t, os.MkdirAll(vendor, 0o755))
	assert.NilError(t, os.WriteFile(filepath.Join(vendor, "foo.go"), []byte(src), 0o600))

	e := &GoEnumerator{}
	mutations, err := e.Enumerate(context.Background(), dir)
	assert.NilError(t, err)
	assert.Equal(t, len(mutations), 0)
}

func TestGoEnumerator_deterministicIDs(t *testing.T) {
	src := `package foo
func f(a, b int) bool { return a < b }
`
	dir := t.TempDir()
	assert.NilError(t, os.WriteFile(filepath.Join(dir, "foo.go"), []byte(src), 0o600))

	e := &GoEnumerator{}
	first, err := e.Enumerate(context.Background(), dir)
	assert.NilError(t, err)
	second, err := e.Enumerate(context.Background(), dir)
	assert.NilError(t, err)

	assert.Equal(t, len(first), len(second))
	for i := range first {
		assert.Equal(t, first[i].ID, second[i].ID)
		assert.Equal(t, first[i].Operator, second[i].Operator)
	}
}

func TestDetectedStack(t *testing.T) {
	t.Run("from config", func(t *testing.T) {
		cfg := &config.ProjectConfig{}
		cfg.Environment = &envspec.Environment{Stack: "go"}
		assert.Equal(t, detectedStack(t.TempDir(), cfg), "go")
	})
}

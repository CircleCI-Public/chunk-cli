package validate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

func TestTestSuitesTemplate(t *testing.T) {
	t.Run("go.mod yields gotestsum template", func(t *testing.T) {
		dir := t.TempDir()
		assert.NilError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/m\n"), 0o644))

		got := TestSuitesTemplate(dir)
		assert.Assert(t, strings.Contains(got, "go list -f"), "got: %s", got)
		assert.Assert(t, strings.Contains(got, "<< test.atoms >>"), "got: %s", got)
		assert.Assert(t, strings.Contains(got, "junit: test-reports/tests.xml"), "got: %s", got)
	})

	t.Run("pyproject.toml yields pytest template", func(t *testing.T) {
		dir := t.TempDir()
		assert.NilError(t, os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte("[project]\n"), 0o644))

		got := TestSuitesTemplate(dir)
		assert.Assert(t, strings.Contains(got, "pytest --collect-only"), "got: %s", got)
		assert.Assert(t, strings.Contains(got, "<< test.atoms >>"), "got: %s", got)
	})

	t.Run("unknown toolchain returns empty string", func(t *testing.T) {
		dir := t.TempDir()
		assert.NilError(t, os.WriteFile(filepath.Join(dir, "Cargo.toml"), []byte(""), 0o644))

		assert.Equal(t, TestSuitesTemplate(dir), "")
	})

	t.Run("empty directory returns empty string", func(t *testing.T) {
		assert.Equal(t, TestSuitesTemplate(t.TempDir()), "")
	})

	t.Run("go.mod takes precedence over pyproject.toml", func(t *testing.T) {
		dir := t.TempDir()
		assert.NilError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644))
		assert.NilError(t, os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte("[project]\n"), 0o644))

		got := TestSuitesTemplate(dir)
		assert.Assert(t, strings.Contains(got, "go list -f"), "expected Go template, got: %s", got)
	})
}

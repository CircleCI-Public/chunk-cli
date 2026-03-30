package sandbox

import (
	"testing"

	"gotest.tools/v3/assert"
)

func TestResolveEnvVars(t *testing.T) {
	lookup := func(env map[string]string) func(string) (string, bool) {
		return func(name string) (string, bool) {
			v, ok := env[name]
			return v, ok
		}
	}

	t.Run("empty spec returns nil", func(t *testing.T) {
		result, err := ResolveEnvVars("", lookup(nil))
		assert.NilError(t, err)
		assert.Assert(t, result == nil)
	})

	t.Run("single var", func(t *testing.T) {
		result, err := ResolveEnvVars("FOO", lookup(map[string]string{"FOO": "bar"}))
		assert.NilError(t, err)
		assert.Equal(t, result["FOO"], "bar")
		assert.Equal(t, len(result), 1)
	})

	t.Run("multiple vars", func(t *testing.T) {
		result, err := ResolveEnvVars("A,B", lookup(map[string]string{"A": "1", "B": "2"}))
		assert.NilError(t, err)
		assert.Equal(t, result["A"], "1")
		assert.Equal(t, result["B"], "2")
		assert.Equal(t, len(result), 2)
	})

	t.Run("whitespace trimming", func(t *testing.T) {
		result, err := ResolveEnvVars("  FOO , BAR  ", lookup(map[string]string{"FOO": "x", "BAR": "y"}))
		assert.NilError(t, err)
		assert.Equal(t, result["FOO"], "x")
		assert.Equal(t, result["BAR"], "y")
	})

	t.Run("missing var returns error", func(t *testing.T) {
		_, err := ResolveEnvVars("MISSING", lookup(map[string]string{}))
		assert.ErrorContains(t, err, "MISSING")
	})

	t.Run("empty value is allowed", func(t *testing.T) {
		result, err := ResolveEnvVars("EMPTY", lookup(map[string]string{"EMPTY": ""}))
		assert.NilError(t, err)
		assert.Equal(t, result["EMPTY"], "")
	})

	t.Run("trailing comma ignored", func(t *testing.T) {
		result, err := ResolveEnvVars("FOO,", lookup(map[string]string{"FOO": "v"}))
		assert.NilError(t, err)
		assert.Equal(t, len(result), 1)
		assert.Equal(t, result["FOO"], "v")
	})

	t.Run("double comma ignored", func(t *testing.T) {
		result, err := ResolveEnvVars("FOO,,BAR", lookup(map[string]string{"FOO": "a", "BAR": "b"}))
		assert.NilError(t, err)
		assert.Equal(t, len(result), 2)
	})
}

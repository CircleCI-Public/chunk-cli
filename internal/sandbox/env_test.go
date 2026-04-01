package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

func TestParseEnvPairs(t *testing.T) {
	t.Run("empty slice returns nil", func(t *testing.T) {
		result, err := ParseEnvPairs(nil)
		assert.NilError(t, err)
		assert.Assert(t, result == nil)
	})

	t.Run("valid pair", func(t *testing.T) {
		result, err := ParseEnvPairs([]string{"FOO=bar"})
		assert.NilError(t, err)
		assert.Equal(t, result["FOO"], "bar")
		assert.Equal(t, len(result), 1)
	})

	t.Run("multiple pairs", func(t *testing.T) {
		result, err := ParseEnvPairs([]string{"A=1", "B=2"})
		assert.NilError(t, err)
		assert.Equal(t, result["A"], "1")
		assert.Equal(t, result["B"], "2")
		assert.Equal(t, len(result), 2)
	})

	t.Run("empty value allowed", func(t *testing.T) {
		result, err := ParseEnvPairs([]string{"EMPTY="})
		assert.NilError(t, err)
		assert.Equal(t, result["EMPTY"], "")
	})

	t.Run("equals sign in value", func(t *testing.T) {
		result, err := ParseEnvPairs([]string{"FOO=a=b"})
		assert.NilError(t, err)
		assert.Equal(t, result["FOO"], "a=b")
	})

	t.Run("missing equals returns error", func(t *testing.T) {
		_, err := ParseEnvPairs([]string{"NOEQUALS"})
		assert.ErrorContains(t, err, "NOEQUALS")
	})
}

func TestParseEnvFile(t *testing.T) {
	t.Run("simple key value", func(t *testing.T) {
		result, err := ParseEnvFile(strings.NewReader("FOO=bar\n"))
		assert.NilError(t, err)
		assert.Equal(t, result["FOO"], "bar")
	})

	t.Run("blank lines and comments ignored", func(t *testing.T) {
		input := `
# this is a comment
FOO=bar

BAZ=qux
`
		result, err := ParseEnvFile(strings.NewReader(input))
		assert.NilError(t, err)
		assert.Equal(t, result["FOO"], "bar")
		assert.Equal(t, result["BAZ"], "qux")
		assert.Equal(t, len(result), 2)
	})

	t.Run("double quoted value", func(t *testing.T) {
		result, err := ParseEnvFile(strings.NewReader(`FOO="hello world"`))
		assert.NilError(t, err)
		assert.Equal(t, result["FOO"], "hello world")
	})

	t.Run("single quoted value", func(t *testing.T) {
		result, err := ParseEnvFile(strings.NewReader("FOO='hello world'"))
		assert.NilError(t, err)
		assert.Equal(t, result["FOO"], "hello world")
	})

	t.Run("export prefix stripped", func(t *testing.T) {
		result, err := ParseEnvFile(strings.NewReader("export FOO=bar"))
		assert.NilError(t, err)
		assert.Equal(t, result["FOO"], "bar")
	})

	t.Run("duplicate key last wins", func(t *testing.T) {
		result, err := ParseEnvFile(strings.NewReader("FOO=first\nFOO=second\n"))
		assert.NilError(t, err)
		assert.Equal(t, result["FOO"], "second")
	})

	t.Run("invalid line returns error", func(t *testing.T) {
		_, err := ParseEnvFile(strings.NewReader("NOEQUALSSIGN\n"))
		assert.ErrorContains(t, err, "invalid line")
	})
}

func TestLoadEnvFile(t *testing.T) {
	t.Run("file missing returns nil nil", func(t *testing.T) {
		result, err := LoadEnvFile(t.TempDir())
		assert.NilError(t, err)
		assert.Assert(t, result == nil)
	})

	t.Run("file exists and is parsed", func(t *testing.T) {
		dir := t.TempDir()
		err := os.WriteFile(filepath.Join(dir, ".env.local"), []byte("FOO=bar\n"), 0o644)
		assert.NilError(t, err)

		result, err := LoadEnvFile(dir)
		assert.NilError(t, err)
		assert.Equal(t, result["FOO"], "bar")
	})

	t.Run("parse error propagates", func(t *testing.T) {
		dir := t.TempDir()
		err := os.WriteFile(filepath.Join(dir, ".env.local"), []byte("BADLINE\n"), 0o644)
		assert.NilError(t, err)

		_, err = LoadEnvFile(dir)
		assert.ErrorContains(t, err, ".env.local")
	})
}

func TestMergeEnv(t *testing.T) {
	t.Run("later layer wins", func(t *testing.T) {
		result := MergeEnv(
			map[string]string{"FOO": "from-file", "BAR": "from-file"},
			map[string]string{"FOO": "from-flag"},
		)
		assert.Equal(t, result["FOO"], "from-flag")
		assert.Equal(t, result["BAR"], "from-file")
	})

	t.Run("empty layers returns empty map", func(t *testing.T) {
		result := MergeEnv()
		assert.Equal(t, len(result), 0)
	})

	t.Run("nil layer is skipped", func(t *testing.T) {
		result := MergeEnv(nil, map[string]string{"A": "1"})
		assert.Equal(t, result["A"], "1")
	})
}

// TestResolveEnv exercises the full env resolution logic that mirrors the
// command handler: parse flag pairs, optionally load .env.local, then merge.
func TestResolveEnv(t *testing.T) {
	t.Run("flags merge with env file", func(t *testing.T) {
		dir := t.TempDir()
		err := os.WriteFile(filepath.Join(dir, ".env.local"), []byte("FILE_VAR=from-file\nSHARED=from-file\n"), 0o644)
		assert.NilError(t, err)

		flagVars, err := ParseEnvPairs([]string{"FLAG_VAR=from-flag", "SHARED=from-flag"})
		assert.NilError(t, err)

		fileVars, err := LoadEnvFile(dir)
		assert.NilError(t, err)

		result := MergeEnv(fileVars, flagVars)
		assert.Equal(t, result["FILE_VAR"], "from-file")
		assert.Equal(t, result["FLAG_VAR"], "from-flag")
		assert.Equal(t, result["SHARED"], "from-flag") // flag wins
		assert.Equal(t, len(result), 3)
	})

	t.Run("no-env-file skips file loading", func(t *testing.T) {
		dir := t.TempDir()
		err := os.WriteFile(filepath.Join(dir, ".env.local"), []byte("FILE_VAR=should-not-appear\n"), 0o644)
		assert.NilError(t, err)

		flagVars, err := ParseEnvPairs([]string{"FLAG_VAR=from-flag"})
		assert.NilError(t, err)

		// When --no-env-file is set, skip LoadEnvFile entirely.
		noEnvFile := true
		var result map[string]string
		if !noEnvFile {
			fileVars, err := LoadEnvFile(dir)
			assert.NilError(t, err)
			result = MergeEnv(fileVars, flagVars)
		} else {
			result = flagVars
		}

		assert.Equal(t, result["FLAG_VAR"], "from-flag")
		_, hasFileVar := result["FILE_VAR"]
		assert.Assert(t, !hasFileVar, "FILE_VAR should not be present when --no-env-file is set")
		assert.Equal(t, len(result), 1)
	})

	t.Run("multiple flag pairs are all preserved", func(t *testing.T) {
		flagVars, err := ParseEnvPairs([]string{"FOO=bar", "BAZ=qux", "HELLO=world"})
		assert.NilError(t, err)

		assert.Equal(t, flagVars["FOO"], "bar")
		assert.Equal(t, flagVars["BAZ"], "qux")
		assert.Equal(t, flagVars["HELLO"], "world")
		assert.Equal(t, len(flagVars), 3)
	})
}

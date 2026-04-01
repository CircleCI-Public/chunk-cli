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

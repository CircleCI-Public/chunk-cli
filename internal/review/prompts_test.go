package review

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"
)

func writeFile(t *testing.T, dir, name, body string) {
	t.Helper()
	assert.NilError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644))
}

func TestLoadPrompts(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeFile(t, dir, "security.md", "  check for injection\n")
	writeFile(t, dir, "api.TXT", "check the API shape")
	writeFile(t, dir, "blank.md", "\n\t \n")
	writeFile(t, dir, "notes.json", `{"ignored": true}`)
	assert.NilError(t, os.Mkdir(filepath.Join(dir, "nested.md"), 0o755))

	got, err := LoadPrompts(dir)
	assert.NilError(t, err)
	assert.DeepEqual(t, got, []Prompt{
		{Name: "api", Body: "check the API shape"},
		{Name: "security", Body: "check for injection"},
	})
}

func TestLoadPromptsEmpty(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeFile(t, dir, "blank.md", "")

	_, err := LoadPrompts(dir)
	assert.Assert(t, errors.Is(err, ErrNoPrompts), "got %v", err)
}

func TestLoadPromptsMissingDir(t *testing.T) {
	t.Parallel()
	_, err := LoadPrompts(filepath.Join(t.TempDir(), "missing"))
	assert.Assert(t, errors.Is(err, os.ErrNotExist), "got %v", err)
}

func TestPoolSize(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		parallelism, prompts, want int
	}{
		{parallelism: 5, prompts: 3, want: 3},
		{parallelism: 2, prompts: 3, want: 2},
		{parallelism: 0, prompts: 4, want: 4},
		{parallelism: -1, prompts: 4, want: 4},
	} {
		assert.Equal(t, PoolSize(tc.parallelism, tc.prompts), tc.want, "%+v", tc)
	}
}

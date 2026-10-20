package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/gitrepo"
)

func runFactoryStartCmd(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	cmd := newFactoryStartCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetIn(strings.NewReader(""))
	cmd.SetArgs(args)
	cmd.SetContext(t.Context())
	err := cmd.Execute()
	return out.String(), errOut.String(), err
}

// The worktree's path alone is on stdout, so cd "$(chunk factory start ...)"
// lands in it, and the record it prints is the one --continue reads.
func TestFactoryStartPrintsTheWorktreeAndHowToContinue(t *testing.T) {
	project := factoryProject(t)

	stdout, stderr, err := runFactoryStartCmd(t, "add a --verbose flag")
	assert.NilError(t, err, stderr)

	path := strings.TrimSpace(stdout)
	assert.Assert(t, path != "" && !strings.Contains(path, "\n"), "stdout: %q", stdout)
	// The worktree is named for the run.
	runID := filepath.Base(path)
	rec, err := factory.LoadRecord(project, runID)
	assert.NilError(t, err)
	assert.Equal(t, rec.Worktree, path)
	assert.Assert(t, rec.NeedsWork)
	assert.Equal(t, rec.Prompt, "add a --verbose flag")
	assert.Assert(t, strings.Contains(stderr, "chunk factory build --continue "+runID), stderr)
	// This test's stderr is a bytes.Buffer, never a terminal, so the new
	// run-start message stays out of it, same as every other TTY-only line.
	assert.Assert(t, !strings.Contains(stderr, "Chunk will set up a worktree"), stderr)
}

// A lone argument naming an existing file, in another repository entirely,
// is read as the prompt: the run still starts on this project, and the
// file's own repo is never touched. The record keeps the file's path.
func TestFactoryStartReadsAFileArgumentAsThePromptFromAnotherRepo(t *testing.T) {
	project := factoryProject(t)

	otherRepo := gitrepo.SetupGitRepo(t, "acme", "notes")
	specPath := filepath.Join(otherRepo, "spec.md")
	spec := "add a --verbose flag"
	assert.NilError(t, os.WriteFile(specPath, []byte(spec), 0o644))

	stdout, stderr, err := runFactoryStartCmd(t, specPath)
	assert.NilError(t, err, stderr)

	path := strings.TrimSpace(stdout)
	runID := filepath.Base(path)
	rec, err := factory.LoadRecord(project, runID)
	assert.NilError(t, err)
	assert.Equal(t, rec.Prompt, spec)
	assert.Equal(t, rec.SpecOrigin, specPath)

	// The file's own repo is untouched.
	got, err := os.ReadFile(specPath)
	assert.NilError(t, err)
	assert.Equal(t, string(got), spec)
}

func TestFactoryStartJSON(t *testing.T) {
	project := factoryProject(t)

	stdout, stderr, err := runFactoryStartCmd(t, "add a --verbose flag", "--json")
	assert.NilError(t, err, stderr)

	var rec factory.Record
	assert.NilError(t, json.Unmarshal([]byte(stdout), &rec))
	assert.Assert(t, rec.NeedsWork)
	stored, err := factory.LoadRecord(project, rec.RunID)
	assert.NilError(t, err)
	assert.DeepEqual(t, stored, rec)
	assert.Assert(t, !strings.Contains(stderr, "Chunk will set up a worktree"), stderr)
}

func TestFactoryStartRequiresAPrompt(t *testing.T) {
	factoryProject(t)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{args: []string{}, want: "The prompt on stdin is empty."},
		{args: []string{"   "}, want: "Pass the prompt as one argument or on stdin."},
	} {
		_, _, err := runFactoryStartCmd(t, tc.args...)
		var ue *userError
		assert.Assert(t, errors.As(err, &ue), "args %q: got %v", tc.args, err)
		assert.Equal(t, ue.UserMessage(), tc.want, "args %q", tc.args)
		assert.Equal(t, ue.UserExitCode(), ExitBadArgs, "args %q", tc.args)
	}
}

// A diff on stdin is the work so far, applied in the worktree, and the prompt
// comes from the argument.
func TestFactoryStartFromADiffOnStdin(t *testing.T) {
	factoryProject(t)
	cmd := newFactoryStartCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetIn(strings.NewReader("diff --git a/flag.go b/flag.go\nnew file mode 100644\n--- /dev/null\n+++ b/flag.go\n@@ -0,0 +1 @@\n+package main\n"))
	cmd.SetArgs([]string{"--diff", "-", "add a flag"})
	cmd.SetContext(t.Context())
	assert.NilError(t, cmd.Execute(), errOut.String())

	got, err := os.ReadFile(filepath.Join(strings.TrimSpace(out.String()), "flag.go"))
	assert.NilError(t, err)
	assert.Equal(t, string(got), "package main\n")
	assert.Assert(t, !strings.Contains(errOut.String(), "Chunk will set up a worktree"), errOut.String())
}

// A missing path with a spec-like extension, a directory, a binary file and
// an oversize file are each refused before any worktree is made.
func TestFactoryStartRefusesUnusableFileArguments(t *testing.T) {
	factoryProject(t)
	dir := t.TempDir()
	binary := filepath.Join(dir, "spec.md")
	assert.NilError(t, os.WriteFile(binary, []byte("bin\x00ary"), 0o644))
	oversize := filepath.Join(dir, "big.txt")
	assert.NilError(t, os.WriteFile(oversize, make([]byte, maxPromptFileSize+1), 0o644))

	for _, tc := range []struct {
		name string
		arg  string
		want string
	}{
		{name: "missing markdown path", arg: filepath.Join(dir, "missing.md"), want: "file not found"},
		{name: "directory", arg: dir, want: "is a directory"},
		{name: "binary file", arg: binary, want: "looks like a binary file"},
		{name: "oversize file", arg: oversize, want: "too large"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := runFactoryStartCmd(t, tc.arg)
			var ue *userError
			assert.Assert(t, errors.As(err, &ue), "got %v", err)
			assert.Assert(t, strings.Contains(ue.UserMessage(), tc.want), ue.UserMessage())
			assert.Assert(t, strings.Contains(ue.Suggestion(), "chunk factory start < file.md"), ue.Suggestion())
			assert.Equal(t, ue.UserExitCode(), ExitBadArgs)
		})
	}
}

// With --diff -, the prompt can only be the argument, and that argument gets
// the same file treatment as any other prompt argument.
func TestFactoryStartFromADiffOnStdinReadsAFileArgumentAsThePrompt(t *testing.T) {
	project := factoryProject(t)
	specPath := filepath.Join(t.TempDir(), "spec.md")
	assert.NilError(t, os.WriteFile(specPath, []byte("add a flag"), 0o644))

	cmd := newFactoryStartCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetIn(strings.NewReader("diff --git a/flag.go b/flag.go\nnew file mode 100644\n--- /dev/null\n+++ b/flag.go\n@@ -0,0 +1 @@\n+package main\n"))
	cmd.SetArgs([]string{"--diff", "-", specPath})
	cmd.SetContext(t.Context())
	assert.NilError(t, cmd.Execute(), errOut.String())

	path := strings.TrimSpace(out.String())
	runID := filepath.Base(path)
	rec, err := factory.LoadRecord(project, runID)
	assert.NilError(t, err)
	assert.Equal(t, rec.Prompt, "add a flag")
	assert.Equal(t, rec.SpecOrigin, specPath)
}

func TestFactoryStartRefusesAnUnusableDiff(t *testing.T) {
	factoryProject(t)
	empty := filepath.Join(t.TempDir(), "empty.diff")
	assert.NilError(t, os.WriteFile(empty, nil, 0o600))
	for _, tc := range []struct {
		args []string
		want string
	}{
		{args: []string{"--diff", "-"}, want: "With --diff -, pass the prompt as an argument."},
		{args: []string{"--diff", "-", "-"}, want: "With --diff -, pass the prompt as an argument."},
		{args: []string{"--diff", empty, "add a flag"}, want: "The diff is empty, so there is nothing to refine."},
	} {
		_, _, err := runFactoryStartCmd(t, tc.args...)
		var ue *userError
		assert.Assert(t, errors.As(err, &ue), "args %q: got %v", tc.args, err)
		assert.Equal(t, ue.UserMessage(), tc.want, "args %q", tc.args)
		assert.Equal(t, ue.UserExitCode(), ExitBadArgs, "args %q", tc.args)
	}
}

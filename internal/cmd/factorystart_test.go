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

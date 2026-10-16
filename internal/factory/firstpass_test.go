package factory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/claudecode"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
)

// fakeClaude puts a claude on PATH that runs script, so a first pass runs
// without Claude Code or an API key. The script sees the prompt on stdin and
// its arguments in $@.
func fakeClaude(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	err := os.WriteFile(filepath.Join(dir, "claude"), []byte("#!/bin/sh\n"+script), 0o755)
	assert.NilError(t, err)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// A first pass is Claude Code editing the run's worktree, and its edits are
// the work a continued run picks up.
func TestFirstPassLeavesClaudesEditsInTheWorktree(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
	ctx := context.Background()
	root := newProject(t)
	before := gitOutput(t, root, "status", "--porcelain")
	rec, err := Start(ctx, root, Handoff{Prompt: "add a flag"})
	assert.NilError(t, err)
	args := filepath.Join(t.TempDir(), "args")
	fakeClaude(t, `echo "$@ hooks=$CHUNK_HOOKS_DISABLED" > `+args+`
cat > prompt.txt
echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file_path":"prompt.txt"}}]}}'
echo '{"type":"result","result":"Wrote prompt.txt","total_cost_usd":0.25}'
`)

	var activity []claudecode.Activity
	turn, err := FirstPass(ctx, rec, "sonnet", func(a claudecode.Activity) { activity = append(activity, a) })
	assert.NilError(t, err)

	assert.Equal(t, turn.Summary, "Wrote prompt.txt")
	assert.Equal(t, turn.CostUSD, 0.25)
	assert.DeepEqual(t, activity, []claudecode.Activity{{Tool: "Write", Detail: "prompt.txt"}})
	assert.Equal(t, readFile(t, rec.Worktree, "prompt.txt"), "add a flag", "claude is sent the prompt")
	gotArgs := readFile(t, filepath.Dir(args), "args")
	assert.Assert(t, strings.Contains(gotArgs, "--permission-mode acceptEdits"), gotArgs)
	assert.Assert(t, strings.Contains(gotArgs, "hooks=1"), "chunk's hooks are off for the pass: %s", gotArgs)
	assert.Assert(t, strings.Contains(gotArgs, "--model sonnet"), "claude is given the model: %s", gotArgs)
	assert.Equal(t, gitOutput(t, root, "status", "--porcelain"), before, "the developer's checkout is untouched")

	stat, err := WorkStat(ctx, rec)
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(stat, "prompt.txt | 1 +"), stat)
	assert.Assert(t, strings.HasSuffix(stat, "1 file changed, 1 insertion(+)"), stat)

	diff, err := WorkDiff(ctx, rec)
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(diff, "+++ b/prompt.txt\n@@ -0,0 +1 @@\n+add a flag"), diff)
}

func TestWorkStatIsEmptyWithNoWork(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
	ctx := context.Background()
	rec, err := Start(ctx, newProject(t), Handoff{Prompt: "add a flag"})
	assert.NilError(t, err)

	stat, err := WorkStat(ctx, rec)
	assert.NilError(t, err)
	assert.Equal(t, stat, "")
}

// Why claude failed is in its result, or on stderr when it has none.
func TestFirstPassReportsWhyClaudeFailed(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
	ctx := context.Background()
	rec, err := Start(ctx, newProject(t), Handoff{Prompt: "add a flag"})
	assert.NilError(t, err)
	for _, tc := range []struct {
		name, script, want string
	}{
		{name: "exit", script: "echo 'model unavailable' >&2\nexit 3", want: "claude exited 3: model unavailable"},
		{name: "error result", script: `echo '{"type":"result","is_error":true,"result":"Prompt is too long"}'`, want: "Prompt is too long"},
		{name: "no result", script: "exit 0", want: "first pass ended without a result"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeClaude(t, tc.script)
			_, err := FirstPass(ctx, rec, "", nil)
			assert.ErrorContains(t, err, tc.want)
		})
	}
}

func TestFirstPassWithoutClaude(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
	ctx := context.Background()
	rec, err := Start(ctx, newProject(t), Handoff{Prompt: "add a flag"})
	assert.NilError(t, err)
	t.Setenv("PATH", t.TempDir())

	assert.Assert(t, errors.Is(CheckClaude(), ErrNoClaude))
	_, err = FirstPass(ctx, rec, "", nil)
	assert.Assert(t, errors.Is(err, ErrNoClaude), "got %v", err)
}

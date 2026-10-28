package factory

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

// NamePlan sends claude the developer's message and returns its reply as
// the name, run with nothing but the message to read.
func TestNamePlanAsksClaudeForAName(t *testing.T) {
	out := t.TempDir()
	fakeClaude(t, `printf '%s\n' "$@" > `+out+`/args
pwd -P > `+out+`/cwd
echo "$CHUNK_HOOKS_DISABLED" > `+out+`/hooks
cat > `+out+`/stdin
echo '  dry-run-flag  '
`)
	msg := "Add a --dry-run flag\nmore detail"

	name, err := NamePlan(context.Background(), msg)
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(name, "dry-run-flag"))

	args := strings.Split(strings.TrimSpace(readFile(t, out, "args")), "\n")
	for _, want := range [][]string{
		{"-p"},
		{"--model", "haiku"},
		{"--output-format", "text"},
		{"--no-session-persistence"},
		{"--tools", ""},
		{"--strict-mcp-config"},
	} {
		assert.Check(t, containsRun(args, want), "args %q lack %q", args, want)
	}
	tmp, err := filepath.EvalSymlinks(os.TempDir())
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(strings.TrimSpace(readFile(t, out, "cwd")), tmp))
	assert.Check(t, cmp.Equal(strings.TrimSpace(readFile(t, out, "hooks")), "1"))
	assert.Check(t, cmp.Equal(readFile(t, out, "stdin"), msg))
}

// NamePlan fails, rather than giving a name, when claude does not give one.
func TestNamePlanFails(t *testing.T) {
	for _, tc := range []struct {
		name   string
		script string // "" for no claude on PATH
		want   error  // nil for any error
	}{
		{
			name: "no claude",
			want: ErrNoClaude,
		},
		{
			name:   "exits non-zero",
			script: "echo 'model unavailable' >&2\nexit 1",
		},
		{
			name:   "hangs",
			script: "sleep 30",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setVar(t, &nameTimeout, 100*time.Millisecond)
			if tc.script == "" {
				t.Setenv("PATH", t.TempDir())
			} else {
				fakeClaude(t, tc.script)
			}
			start := time.Now()
			_, err := NamePlan(context.Background(), "add a flag")
			assert.Check(t, err != nil)
			if tc.want != nil {
				assert.Check(t, cmp.ErrorIs(err, tc.want))
			}
			assert.Check(t, time.Since(start) < 5*time.Second, "NamePlan waited %s on claude", time.Since(start))
		})
	}
}

// containsRun reports whether want appears as consecutive elements of args.
func containsRun(args, want []string) bool {
	for i := range len(args) - len(want) + 1 {
		if slices.Equal(args[i:i+len(want)], want) {
			return true
		}
	}
	return false
}

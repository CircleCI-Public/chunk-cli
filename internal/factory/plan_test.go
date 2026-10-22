package factory

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

// A planning turn reads other branches and pull requests, so claude is given
// the read-only git and gh commands rather than having Bash taken away.
func TestPlanTurnAllowsReadOnlyShell(t *testing.T) {
	ctx := context.Background()
	planDir := t.TempDir()
	args := filepath.Join(t.TempDir(), "args")
	fakeClaude(t, `echo "$@" > `+args+`
cat > /dev/null
echo '{"type":"result","result":"Wrote the plan"}'
`)
	s := &PlanSession{Dir: newProject(t), PlanPath: filepath.Join(planDir, "plan.md")}

	reply, err := s.Turn(ctx, "plan a change", nil)
	assert.NilError(t, err)
	assert.Equal(t, reply, "Wrote the plan")

	got := readFile(t, filepath.Dir(args), "args")
	assert.Assert(t, strings.Contains(got, "--permission-mode acceptEdits"), got)
	assert.Assert(t, strings.Contains(got, "--allowedTools "+strings.Join(planTools, ",")), got)
	assert.Assert(t, strings.Contains(got, "--disallowedTools "+strings.Join(planDisallowed, ",")), got)
	assert.Assert(t, !slices.Contains(strings.Fields(got), "Bash"), "Bash is allowlisted, not taken away: %s", got)
	assert.Assert(t, strings.Contains(got, "--add-dir "+planDir), got)
}

// The commands planning may and may not run, written out rather than taken
// from the lists under test, so a slip in a pattern is a failing test.
func TestPlanAllowsReadingAndDeniesWriting(t *testing.T) {
	assert.DeepEqual(t, planTools, []string{
		"Bash(git log:*)", "Bash(git diff:*)", "Bash(git show:*)", "Bash(git status:*)",
		"Bash(git branch:*)", "Bash(git rev-parse:*)", "Bash(git ls-remote:*)",
		"Bash(git ls-files:*)", "Bash(git ls-tree:*)", "Bash(git fetch:*)",
		"Bash(gh pr view:*)", "Bash(gh pr diff:*)", "Bash(gh pr list:*)", "Bash(gh pr checks:*)",
	})
	assert.DeepEqual(t, planDisallowed, []string{
		"Bash(git commit:*)", "Bash(git stash:*)", "Bash(git reset:*)",
		"Bash(git checkout:*)", "Bash(git switch:*)", "Bash(git rebase:*)",
		"Bash(git push:*)", "Bash(git merge:*)", "Bash(git pull:*)", "Bash(git clean:*)", "Bash(git restore:*)",
		"Bash(gh pr comment:*)", "Bash(gh pr merge:*)", "Bash(gh pr review:*)", "Bash(gh pr close:*)",
		"Bash(gh pr edit:*)", "Bash(gh pr create:*)", "Bash(gh api:*)",
	})
}

// The allowlist and the backstop must not contradict each other: a command
// claude is told it may run is never one the backstop denies.
func TestPlanToolsAreNotDisallowed(t *testing.T) {
	for _, tool := range planTools {
		assert.Assert(t, !slices.Contains(planDisallowed, tool), "%s is both allowed and disallowed", tool)
	}
}

// The backstop keeps the implementer's rules, which the plan session shares:
// nothing that moves work out of the working tree.
func TestPlanDisallowedKeepsTheGitBackstop(t *testing.T) {
	for _, tool := range disallowedGit {
		assert.Assert(t, slices.Contains(planDisallowed, tool), "%s is missing from the plan backstop", tool)
	}
	assert.Assert(t, !slices.Contains(disallowedGit, "Bash(git push:*)"), "planDisallowed must not have appended into disallowedGit")
}

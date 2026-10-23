package factory

import (
	"context"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

// A planning turn reads other branches and pull requests, so claude is given
// the read-only git and gh commands rather than having Bash taken away, and
// nothing but those rules: not acceptEdits, and not the developer's settings.
func TestPlanTurnAllowsReadOnlyShell(t *testing.T) {
	ctx := context.Background()
	planDir := t.TempDir()
	planPath := filepath.Join(planDir, "plan.md")
	args := filepath.Join(t.TempDir(), "args")
	envFile := filepath.Join(t.TempDir(), "env")
	fakeClaude(t, `echo "$@" > `+args+`
echo "$GIT_TERMINAL_PROMPT" > `+envFile+`
cat > /dev/null
echo '{"type":"result","result":"Wrote the plan"}'
`)
	s := &PlanSession{Dir: newProject(t), PlanPath: planPath}

	reply, err := s.Turn(ctx, "plan a change", nil)
	assert.NilError(t, err)
	assert.Equal(t, reply, "Wrote the plan")

	got := readFile(t, filepath.Dir(args), "args")
	allowed := strings.Join(append(slices.Clone(planTools), planEditRule(planPath)), ",")
	assert.Assert(t, strings.Contains(got, "--allowedTools "+allowed), got)
	assert.Assert(t, strings.Contains(got, "--disallowedTools "+strings.Join(planDisallowed, ",")), got)
	assert.Assert(t, !strings.Contains(got, "acceptEdits"), "acceptEdits lets claude touch, move and remove files: %s", got)
	assert.Assert(t, strings.Contains(got, "--setting-sources  "), "no settings load, so no allow rule but ours applies: %s", got)
	assert.Assert(t, !slices.Contains(strings.Fields(got), "Bash"), "Bash is allowlisted, not taken away: %s", got)
	assert.Assert(t, strings.Contains(got, "--add-dir "+planDir), got)
	assert.Equal(t, strings.TrimSpace(readFile(t, filepath.Dir(envFile), "env")), "0")
}

func TestPlanEditRuleNamesOnlyThePlanFile(t *testing.T) {
	assert.Equal(t, planEditRule("/data/plans/p.md"), "Edit(//data/plans/p.md)")
}

// ruleMatches is how claude matches a Bash rule against a command, as far as
// planning needs: a rule is the command exactly, or with a :* suffix the
// command and anything after it, and a * elsewhere matches any run of words.
func ruleMatches(rule, command string) bool {
	body := strings.TrimSuffix(strings.TrimPrefix(rule, "Bash("), ")")
	if prefix, ok := strings.CutSuffix(body, ":*"); ok {
		return command == prefix || strings.HasPrefix(command, prefix+" ")
	}
	re := "^" + strings.ReplaceAll(regexp.QuoteMeta(body), `\*`, ".*") + "$"
	return regexp.MustCompile(re).MatchString(command)
}

// runs is whether claude would run command: some allow rule matches it and no
// deny rule does, since a deny rule wins.
func runs(command string) bool {
	return slices.ContainsFunc(planTools, func(r string) bool { return ruleMatches(r, command) }) &&
		!slices.ContainsFunc(planDisallowed, func(r string) bool { return ruleMatches(r, command) })
}

// Planning may read other branches and pull requests.
func TestPlanRunsWhatReadsAndNothingElse(t *testing.T) {
	for _, c := range []string{
		"git log --oneline -5", "git log origin/topic", "git diff main...origin/topic", "git show origin/topic:internal/a.go",
		"git status", "git rev-parse origin/topic", "git ls-files", "git ls-tree origin/topic",
		"git fetch origin", "git branch", "git branch -r", "git branch -a", "git branch --show-current",
		"gh pr view 12", "gh pr diff 12", "gh pr list", "gh pr checks 12",
	} {
		assert.Assert(t, runs(c), "%q should run", c)
	}
}

// The commands an allowed one can be turned into, which write a file, run a
// program, delete or move a branch, or reach out of planning.
func TestPlanDeniesWhatWritesOrRunsAProgram(t *testing.T) {
	for _, c := range []string{
		"git ls-remote --upload-pack=touch /x .", "git ls-remote origin",
		"git fetch --upload-pack=touch /x origin", "git fetch origin +topic:refs/heads/main", "git fetch origin topic",
		"git diff --output=/x HEAD", "git log --output /x", "git show --output=/x HEAD", "git diff HEAD --output=/x",
		"git branch -D main", "git branch -f main HEAD", "git branch -m main other", "git branch -r -D x", "git branch --list -D x",
		"git checkout main", "git push origin main", "git commit -am x",
		"gh pr comment 12 -b x", "gh pr merge 12", "gh api repos/o/r/pulls/1 -X PATCH",
		"chunk factory build plan.md", "chunk sidecar create", "touch /x", "rm -rf /",
	} {
		assert.Assert(t, !runs(c), "%q must not run", c)
	}
}

// The backstop keeps the implementer's rules, which the plan session shares.
func TestPlanDisallowedKeepsTheGitBackstop(t *testing.T) {
	for _, tool := range disallowedGit {
		assert.Assert(t, slices.Contains(planDisallowed, tool), "%s is missing from the plan backstop", tool)
	}
}

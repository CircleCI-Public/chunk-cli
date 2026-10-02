package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/review"
	"github.com/CircleCI-Public/chunk-cli/internal/watchd"
)

// TestFactoryChecksOnlyToleratesMissingDefaultReviews guards against a
// mistyped --reviews silently running no reviews at all.
func TestFactoryChecksOnlyToleratesMissingDefaultReviews(t *testing.T) {
	workDir := t.TempDir()
	cfg := &config.ProjectConfig{Commands: []config.Command{{Name: "test", Run: "go test ./..."}}}

	prompts, commands, err := factoryChecks(workDir, "", cfg, false)
	assert.NilError(t, err, "a missing default directory is fine when there are commands")
	assert.Equal(t, len(prompts), 0)
	assert.Equal(t, len(commands), 1)

	_, _, err = factoryChecks(workDir, filepath.Join(workDir, "reviws"), cfg, false)
	assert.ErrorContains(t, err, "reviws")

	empty := filepath.Join(workDir, "empty")
	assert.NilError(t, os.Mkdir(empty, 0o755))
	_, _, err = factoryChecks(workDir, empty, cfg, false)
	assert.ErrorIs(t, err, review.ErrNoPrompts)
}

func TestKeepWorkHint(t *testing.T) {
	// A branch that starts at the developer's HEAD merges.
	clean := factory.Worktree{Branch: "chunk/factory/run-1", Baseline: "abc", Head: "abc"}
	assert.Equal(t, keepWorkHint(clean), "Merge it with: git merge chunk/factory/run-1")

	// One that starts from their uncommitted work would collide with that work
	// in their checkout, so only the run's own changes are applied.
	dirty := factory.Worktree{Branch: "chunk/factory/run-1", Baseline: "def", Head: "abc"}
	assert.Equal(t, keepWorkHint(dirty), "Apply it with: git diff --binary def chunk/factory/run-1 | git apply")
}

// A stuck run's record ends on a round that was never checked; its outcome is
// the last round that was.
func TestFactoryOutcomeIsTheLastCheckedRound(t *testing.T) {
	bug := review.Finding{File: "main.go", Line: 3, Severity: "high", Body: "nil deref"}
	detail := watchd.SessionDetail{
		Session: watchd.Session{
			Factory: &watchd.FactoryRun{Result: string(factory.ResultStuck), Rounds: 1},
			Rounds:  []watchd.Round{{Number: 1}, {Number: 2}},
		},
		Details: []watchd.RoundDetail{
			{Number: 1, Results: []watchd.ReviewResult{{Prompt: "bugs", Status: "failed", Findings: []review.Finding{bug}}}},
			{Number: 2},
		},
	}
	o := factoryOutcome(detail)
	assert.Equal(t, o.Rounds, 1)
	assert.Equal(t, len(o.Checks), 1)
	assert.Equal(t, o.Checks[0].Name, "bugs")
	assert.Equal(t, len(o.Checks[0].Findings), 1)
}

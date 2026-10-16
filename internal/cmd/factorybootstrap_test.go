package cmd

import (
	"errors"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/factory/eval"
)

// Bootstrap sits beside build, not under it. Registered on the build command
// instead it still compiles, and answers at `chunk factory build bootstrap`,
// which is not what the docs or the nothing-to-check suggestion name.
func TestFactoryBootstrapIsRegisteredUnderFactory(t *testing.T) {
	cmd, _, err := newFactoryCmd().Find([]string{"bootstrap"})
	assert.NilError(t, err)
	assert.Equal(t, cmd.Name(), "bootstrap")
}

// An explicit --template is a name to look up, even when it is empty. Gating
// on the value instead of the flag sends `--template ""` down the sidecar path,
// where it boots an environment and runs Claude.
func TestFactoryBootstrapEmptyTemplateIsUnknown(t *testing.T) {
	t.Chdir(t.TempDir())
	cmd := newFactoryBootstrapCmd()
	cmd.SetArgs([]string{"--template", "", "--output", filepath.Join(t.TempDir(), "reviews")})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true

	err := cmd.Execute()

	assert.ErrorIs(t, err, factory.ErrUnknownTemplate)
}

// Scoring from bootstrap stays out of its help while factory eval is hidden.
func TestFactoryBootstrapEvalCommitsIsHidden(t *testing.T) {
	f := newFactoryBootstrapCmd().Flags().Lookup("eval-commits")
	assert.Assert(t, f != nil)
	assert.Assert(t, f.Hidden)
}

func TestFactoryEvalIsRegisteredUnderFactoryAndHidden(t *testing.T) {
	cmd, _, err := newFactoryCmd().Find([]string{"eval"})
	assert.NilError(t, err)
	assert.Equal(t, cmd.Name(), "eval")
	assert.Assert(t, cmd.Hidden)
}

func TestReadyScoringOnlyAfterBothPartsSucceed(t *testing.T) {
	cases := &evalRun{Root: "/repo", Suite: eval.Suite{Cases: []eval.Case{{Name: "c"}}}, Source: "1 commit(s) on HEAD"}
	wrote := bootstrapOutcome{paths: []string{"a.md"}}
	run := evalRun{PromptsDir: "/repo/.chunk/reviews", Image: "snap-1"}

	got := readyScoring(cases, wrote, nil, run)
	assert.Assert(t, got != nil)
	assert.Equal(t, got.Root, "/repo")
	assert.Equal(t, len(got.Suite.Cases), 1)
	assert.Equal(t, got.Image, "snap-1")
	assert.Equal(t, got.PromptsDir, "/repo/.chunk/reviews")

	assert.Assert(t, readyScoring(nil, wrote, nil, run) == nil, "not asked for")
	assert.Assert(t, readyScoring(cases, wrote, errors.New("sidecar image failed"), run) == nil, "a part failed")
	assert.Assert(t, readyScoring(cases, bootstrapOutcome{}, nil, run) == nil, "no prompts written")
}

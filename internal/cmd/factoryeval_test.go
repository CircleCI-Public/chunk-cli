package cmd

import (
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/factory/eval"
)

// A run in which every case errored measured nothing, and must not exit as
// though it had; one in which only some did is still a score.
func TestNoneScored(t *testing.T) {
	assert.ErrorContains(t, noneScored(eval.Summary{Runs: 4, Errored: 4}), "all 4 case run(s) errored")
	assert.NilError(t, noneScored(eval.Summary{Runs: 4, Scored: 1, Errored: 3}))
	assert.NilError(t, noneScored(eval.Summary{}))
}

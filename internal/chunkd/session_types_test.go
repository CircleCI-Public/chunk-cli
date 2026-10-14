package chunkd

import (
	"testing"

	"gotest.tools/v3/assert"
)

// Only a run that stopped failing with committed work on a branch has
// something for --continue to pick up.
func TestContinuableNeedsFailingCommittedWorkOnABranch(t *testing.T) {
	cases := map[string]struct {
		run  FactoryRun
		want bool
	}{
		"exhausted":     {FactoryRun{Result: ResultExhausted, Committed: true, Branch: "chunk/factory/abc"}, true},
		"stuck":         {FactoryRun{Result: ResultStuck, Committed: true, Branch: "chunk/factory/abc"}, true},
		"passed":        {FactoryRun{Result: ResultPassed, Committed: true, Branch: "chunk/factory/abc"}, false},
		"no change":     {FactoryRun{Result: ResultNoChange, Committed: true, Branch: "chunk/factory/abc"}, false},
		"not committed": {FactoryRun{Result: ResultExhausted, Branch: "chunk/factory/abc"}, false},
		"no branch":     {FactoryRun{Result: ResultExhausted, Committed: true}, false},
		"still running": {FactoryRun{Committed: true, Branch: "chunk/factory/abc"}, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.run.Continuable(), tc.want)
		})
	}
}

func TestContinueCommandNamesTheRunFromItsBranch(t *testing.T) {
	f := FactoryRun{Branch: "chunk/factory/add-verbose/abc123"}
	assert.Equal(t, f.ContinueCommand(), "chunk factory build --continue abc123")
}

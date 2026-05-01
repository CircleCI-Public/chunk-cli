package validate

import (
	"os"
	"testing"

	"gotest.tools/v3/assert"
)

func TestSaveLoadResults(t *testing.T) {
	treeSHA := "abc123def456abc123def456abc123def456abc1"
	t.Cleanup(func() { _ = os.Remove(resultsPath(treeSHA)) })

	want := []CommandResult{
		{Name: "test", Passed: true},
		{Name: "lint", Passed: false},
	}

	err := SaveResults(treeSHA, want)
	assert.NilError(t, err)

	got, found, err := LoadResults(treeSHA)
	assert.NilError(t, err)
	assert.Assert(t, found)
	assert.DeepEqual(t, got, want)
}

func TestLoadResults_NotFound(t *testing.T) {
	results, found, err := LoadResults("0000000000000000000000000000000000000000")
	assert.NilError(t, err)
	assert.Assert(t, !found)
	assert.Assert(t, results == nil)
}

package hashutil_test

import (
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/hashutil"
)

func TestSumPartsSameInputsEqual(t *testing.T) {
	assert.Equal(t, hashutil.SumParts("a", "b"), hashutil.SumParts("a", "b"))
}

// TestSumPartsBoundaryCollision is the property the length prefix exists for:
// without it these two splits would concatenate to the same bytes, so two
// different cache keys would share an entry.
func TestSumPartsBoundaryCollision(t *testing.T) {
	assert.Assert(t, hashutil.SumParts("ab", "c") != hashutil.SumParts("a", "bc"),
		"different part splits must produce different digests")
}

func TestSumPartsEmptyPartCounts(t *testing.T) {
	assert.Assert(t, hashutil.SumParts("", "a") != hashutil.SumParts("a"),
		"an empty part must still change the digest")
}

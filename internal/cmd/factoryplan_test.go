package cmd

import (
	"testing"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func TestPlanOriginNamesTheFactoryPlan(t *testing.T) {
	assert.Check(t, cmp.Equal(planOrigin("1234"), "factory plan 1234"))
	assert.Check(t, cmp.Equal(planOrigin(""), ""))
}

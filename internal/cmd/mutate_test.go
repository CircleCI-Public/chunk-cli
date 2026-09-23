package cmd

import (
	"errors"
	"testing"

	"gotest.tools/v3/assert"
)

func TestMutateParallelFlag(t *testing.T) {
	cmd := newMutateCmd()
	assert.Assert(t, cmd.Flags().Lookup("parallel") != nil)
	assert.Assert(t, cmd.Flags().Lookup("parallelism") == nil)
}

func TestMutationRunError(t *testing.T) {
	assert.NilError(t, mutationRunError(0))

	err := mutationRunError(2)
	assert.ErrorContains(t, err, "mutation run completed with 2 error(s)")

	var userErr *userError
	assert.Assert(t, errors.As(err, &userErr))
	assert.Equal(t, userErr.msg, "2 mutation(s) could not be tested.")
}

func TestMutationPoolSize(t *testing.T) {
	assert.Equal(t, mutationPoolSize(100, 20), 20)
	assert.Equal(t, mutationPoolSize(10, 30), 10)
}

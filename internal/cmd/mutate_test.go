package cmd

import (
	"errors"
	"testing"
	"time"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/mutate"
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

func TestMutationScheduleErrorExplainsBaselineFailure(t *testing.T) {
	err := mutationScheduleError(&mutate.BaselineError{ExitCode: 1, Output: "FAIL TestThing\n"}, "task test")

	var userErr *userError
	assert.Assert(t, errors.As(err, &userErr))
	assert.Equal(t, userErr.msg, "Tests fail before any mutation was applied.")
	assert.Equal(t, userErr.detail, "FAIL TestThing\n")
	assert.Assert(t, userErr.suggestion != "")
	assert.ErrorContains(t, err, "baseline test failed (exit 1)")
}

func TestMutationScheduleErrorExplainsBaselineTimeout(t *testing.T) {
	err := mutationScheduleError(&mutate.BaselineError{TimedOut: true, Timeout: time.Minute}, "task test")

	var userErr *userError
	assert.Assert(t, errors.As(err, &userErr))
	assert.Assert(t, userErr.hideDetail)
	assert.ErrorContains(t, err, "baseline test timed out after 1m0s")
}

func TestMutationScheduleErrorWrapsOtherErrors(t *testing.T) {
	err := mutationScheduleError(errors.New("acquire sidecar: pool exhausted"), "task test")

	var userErr *userError
	assert.Assert(t, !errors.As(err, &userErr))
	assert.ErrorContains(t, err, "run: acquire sidecar: pool exhausted")
}

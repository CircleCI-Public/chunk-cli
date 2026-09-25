package review

import (
	"context"
	"errors"
	"testing"

	"gotest.tools/v3/assert"
)

func TestWaitReady(t *testing.T) {
	t.Parallel()
	waitSynced := func(context.Context) error { return nil }

	assert.NilError(t, WaitReady(context.Background(), waitSynced))
}

func TestWaitReadyReportsSyncFailure(t *testing.T) {
	t.Parallel()
	syncErr := errors.New("sync failed")
	waitSynced := func(context.Context) error { return syncErr }

	err := WaitReady(context.Background(), waitSynced)
	assert.Assert(t, errors.Is(err, syncErr), "got %v", err)
}

package review

import (
	"context"
	"errors"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
)

type fakePool struct {
	free     []*sidecar.PoolEntry
	err      error
	released []string
}

func (p *fakePool) Acquire(context.Context) (*sidecar.PoolEntry, error) {
	if len(p.free) == 0 {
		return nil, p.err
	}
	e := p.free[0]
	p.free = p.free[1:]
	return e, nil
}

func (p *fakePool) Release(e *sidecar.PoolEntry) {
	p.released = append(p.released, e.ID)
}

func TestWaitReady(t *testing.T) {
	t.Parallel()
	pool := &fakePool{free: []*sidecar.PoolEntry{{ID: "sb-1"}, {ID: "sb-2"}}}

	assert.NilError(t, WaitReady(context.Background(), pool, 2))
	assert.DeepEqual(t, pool.released, []string{"sb-1", "sb-2"})
}

func TestWaitReadyReleasesOnFailure(t *testing.T) {
	t.Parallel()
	syncErr := errors.New("sync failed")
	pool := &fakePool{free: []*sidecar.PoolEntry{{ID: "sb-1"}}, err: syncErr}

	err := WaitReady(context.Background(), pool, 2)
	assert.Assert(t, errors.Is(err, syncErr), "got %v", err)
	assert.DeepEqual(t, pool.released, []string{"sb-1"})
}

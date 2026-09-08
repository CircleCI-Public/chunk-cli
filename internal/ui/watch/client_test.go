package watch

import (
	"errors"
	"testing"

	"gotest.tools/v3/assert"
)

// fetchSeq returns a fetch func that yields the given results in order, and a
// pointer to the call count.
func fetchSeq(results ...error) (func(Model) (dataMsg, error), *int) {
	calls := 0
	return func(Model) (dataMsg, error) {
		i := calls
		calls++
		if i < len(results) && results[i] != nil {
			return dataMsg{}, results[i]
		}
		return dataMsg{branches: []string{"main"}}, nil
	}, &calls
}

func TestLoadWithRelaunch_successNeverTouchesTheDaemon(t *testing.T) {
	fetch, calls := fetchSeq(nil)
	relaunched := 0
	m := New(nil, false).WithDaemonArgs([]string{"watch", "_daemon"})

	msg := loadWithRelaunch(m, fetch, func([]string) error { relaunched++; return nil })

	_, ok := msg.(dataMsg)
	assert.Assert(t, ok, "want dataMsg, got %T", msg)
	assert.Equal(t, *calls, 1)
	assert.Equal(t, relaunched, 0)
}

func TestLoadWithRelaunch_relaunchesAndRetriesOnce(t *testing.T) {
	// First fetch fails (daemon gone), second succeeds (daemon back).
	fetch, calls := fetchSeq(errors.New("connect to watch daemon: no such file"))
	relaunched := 0
	m := New(nil, false).WithDaemonArgs([]string{"watch", "_daemon"})

	msg := loadWithRelaunch(m, fetch, func([]string) error { relaunched++; return nil })

	_, ok := msg.(dataMsg)
	assert.Assert(t, ok, "want dataMsg after relaunch, got %T", msg)
	assert.Equal(t, relaunched, 1)
	assert.Equal(t, *calls, 2)
}

func TestLoadWithRelaunch_withoutArgsItDoesNotTryToRelaunch(t *testing.T) {
	fetch, calls := fetchSeq(errors.New("boom"))
	relaunched := 0
	m := New(nil, false) // no daemon argv

	msg := loadWithRelaunch(m, fetch, func([]string) error { relaunched++; return nil })

	err, ok := msg.(errMsg)
	assert.Assert(t, ok, "want errMsg, got %T", msg)
	assert.Error(t, err.err, "boom")
	assert.Equal(t, relaunched, 0)
	assert.Equal(t, *calls, 1)
}

func TestLoadWithRelaunch_reportsTheFetchErrorWhenRelaunchFails(t *testing.T) {
	fetch, _ := fetchSeq(errors.New("connect to watch daemon: no such file"))
	m := New(nil, false).WithDaemonArgs([]string{"watch", "_daemon"})

	msg := loadWithRelaunch(m, fetch, func([]string) error { return errors.New("could not spawn") })

	err, ok := msg.(errMsg)
	assert.Assert(t, ok, "want errMsg, got %T", msg)
	// The fetch failure is what the reader is looking at, not the relaunch one.
	assert.Error(t, err.err, "connect to watch daemon: no such file")
}

func TestLoadWithRelaunch_reportsTheRetryErrorWhenBothFetchesFail(t *testing.T) {
	fetch, calls := fetchSeq(errors.New("first"), errors.New("second"))
	m := New(nil, false).WithDaemonArgs([]string{"watch", "_daemon"})

	msg := loadWithRelaunch(m, fetch, func([]string) error { return nil })

	err, ok := msg.(errMsg)
	assert.Assert(t, ok, "want errMsg, got %T", msg)
	assert.Error(t, err.err, "second")
	assert.Equal(t, *calls, 2)
}

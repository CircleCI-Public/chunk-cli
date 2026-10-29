package factoryplan

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/plan"
)

// answer is one answer of the fake daemon to a fetch.
type answer struct {
	log chunkd.SessionLog
	err error
}

// fakeDaemon is the chunk daemon's planner routes: it records what an Agent
// posts and the cursor of each fetch, and answers fetches from answers in
// order, then with no events at the end of the last log it answered with.
type fakeDaemon struct {
	mu        sync.Mutex
	answers   []answer
	afters    []int
	planIDs   []string
	requests  []chunkd.PlannerRequest
	resumes   []chunkd.PlannerResume
	postErr   error
	lastLog   chunkd.SessionLog
	fetchedCh chan struct{} // signalled after each fetch
}

func newFakeDaemon(answers ...answer) *fakeDaemon {
	return &fakeDaemon{answers: answers, fetchedCh: make(chan struct{}, 1)}
}

func (f *fakeDaemon) request(planID string, req chunkd.PlannerRequest) (chunkd.SessionLog, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.planIDs = append(f.planIDs, planID)
	f.requests = append(f.requests, req)
	return chunkd.SessionLog{}, f.postErr
}

func (f *fakeDaemon) resume(planID string, req chunkd.PlannerResume) (chunkd.SessionLog, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.planIDs = append(f.planIDs, planID)
	f.resumes = append(f.resumes, req)
	return chunkd.SessionLog{}, f.postErr
}

func (f *fakeDaemon) fetch(planID string, after int) (chunkd.SessionLog, error) {
	f.mu.Lock()
	defer func() {
		f.mu.Unlock()
		select {
		case f.fetchedCh <- struct{}{}:
		default:
		}
	}()
	f.afters = append(f.afters, after)
	if len(f.answers) == 0 {
		return chunkd.SessionLog{LogID: f.lastLog.LogID, Oldest: f.lastLog.Oldest, Next: f.lastLog.Next}, nil
	}
	a := f.answers[0]
	f.answers = f.answers[1:]
	if a.err == nil {
		f.lastLog = a.log
	}
	return a.log, a.err
}

// fetched is the cursor of each fetch so far.
func (f *fakeDaemon) fetched() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int(nil), f.afters...)
}

func (f *fakeDaemon) posts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests) + len(f.resumes)
}

// newTestAgent is an Agent for plan planID, "" for one not yet created, over
// the fake daemon d, polling until the test ends. It is called in a synctest
// bubble, so the poller's ticks fire as soon as everything else is blocked.
func newTestAgent(t *testing.T, planID string, d *fakeDaemon) (*Agent, *Store) {
	t.Helper()
	store := &Store{planID: planID}
	a := newAgent(t.Context(), "/src", "opus", store, d.request, d.resume, d.fetch)
	t.Cleanup(func() { _ = a.Stop() })
	return a, store
}

// collect is every event a emits before d has been fetched from n times.
// Events are sent unbuffered, so a fetch only follows once what the fetch
// before it read has been taken: all of the first n-1 fetches' events are in
// the result.
func collect(t *testing.T, a *Agent, d *fakeDaemon, n int) []plan.Event {
	t.Helper()
	var got []plan.Event
	for len(d.fetched()) < n {
		select {
		case ev := <-a.Events():
			got = append(got, ev)
		case <-d.fetchedCh:
		}
	}
	return got
}

func logOf(id string, oldest, next int, events ...chunkd.SessionLogEvent) chunkd.SessionLog {
	if events == nil {
		events = []chunkd.SessionLogEvent{}
	}
	return chunkd.SessionLog{LogID: id, Oldest: oldest, Next: next, Events: events}
}

func activity(seq int, text string) chunkd.SessionLogEvent {
	return chunkd.SessionLogEvent{Seq: seq, Kind: chunkd.SessionLogActivity, Text: text}
}

func TestAgentRequestAndResumeEachPostOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := newFakeDaemon()
		a, _ := newTestAgent(t, "plan-1", d)

		err := a.Request(t.Context(), "add a flag")
		assert.NilError(t, err)
		err = a.Resume(t.Context())
		assert.NilError(t, err)

		d.mu.Lock()
		defer d.mu.Unlock()
		assert.Check(t, cmp.DeepEqual(d.requests, []chunkd.PlannerRequest{{ProjectRoot: "/src", Text: "add a flag", Model: "opus"}}))
		assert.Check(t, cmp.DeepEqual(d.resumes, []chunkd.PlannerResume{{ProjectRoot: "/src", Model: "opus"}}))
		assert.Check(t, cmp.DeepEqual(d.planIDs, []string{"plan-1", "plan-1"}))
	})
}

func TestAgentDaemonRefusalsAreSentences(t *testing.T) {
	other := errors.New("decode response: unexpected EOF")
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "refused",
			err:  &chunkd.SessionRefused{Status: 409, Message: "the plan's request has already been sent to its planning session"},
			want: "the plan's request has already been sent to its planning session",
		},
		{
			name: "unreachable",
			err:  fmt.Errorf("%w: dial unix: no such file", chunkd.ErrDaemonUnavailable),
			want: "the chunk daemon is not reachable",
		},
		{
			name: "other",
			err:  other,
			want: other.Error(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				d := newFakeDaemon()
				d.postErr = tt.err
				a, _ := newTestAgent(t, "plan-1", d)

				err := a.Request(t.Context(), "add a flag")
				assert.Check(t, cmp.Error(err, tt.want))
				err = a.Resume(t.Context())
				assert.Check(t, cmp.Error(err, tt.want))
				assert.Check(t, cmp.Equal(d.posts(), 2))
			})
		})
	}
}

func TestAgentCheckStartRefusesBeforeTheDaemon(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := newFakeDaemon()
		a, _ := newTestAgent(t, "plan-1", d)
		checked := 0
		a.CheckStart(func(context.Context) error {
			checked++
			return errors.New("open chunk watch in this plan's local repository to plan or build")
		})

		err := a.Request(t.Context(), "add a flag")
		assert.Check(t, cmp.ErrorContains(err, "local repository"))
		err = a.Resume(t.Context())
		assert.Check(t, cmp.ErrorContains(err, "local repository"))
		assert.Check(t, cmp.Equal(checked, 2))
		assert.Check(t, cmp.Equal(d.posts(), 0))
	})
}

func TestAgentCheckStartRunsBeforeEachCall(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := newFakeDaemon()
		a, _ := newTestAgent(t, "plan-1", d)
		checked := 0
		a.CheckStart(func(context.Context) error { checked++; return nil })

		err := a.Request(t.Context(), "add a flag")
		assert.NilError(t, err)
		err = a.Resume(t.Context())
		assert.NilError(t, err)
		err = a.Resume(t.Context())
		assert.NilError(t, err)
		assert.Check(t, cmp.Equal(checked, 3))
		assert.Check(t, cmp.Equal(d.posts(), 3))
	})
}

func TestAgentWaitsForThePlan(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := newFakeDaemon(answer{log: logOf("log-1", 0, 0)})
		_, store := newTestAgent(t, "", d)

		time.Sleep(20 * pollInterval)
		synctest.Wait()
		assert.Check(t, cmp.Len(d.fetched(), 0))

		store.mu.Lock()
		store.planID = "plan-1"
		store.mu.Unlock()
		<-d.fetchedCh
		assert.Check(t, cmp.DeepEqual(d.fetched()[:1], []int{-1}))
	})
}

func TestAgentPollsFromTheFirstFetchsEnd(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := newFakeDaemon(
			answer{log: logOf("log-1", 0, 5)},
			answer{log: logOf("log-1", 0, 6, activity(5, "Reading the plan"))},
		)
		a, _ := newTestAgent(t, "plan-1", d)

		got := collect(t, a, d, 3)
		assert.Check(t, cmp.DeepEqual(d.fetched()[:3], []int{-1, 5, 6}))
		assert.Check(t, cmp.DeepEqual(got, []plan.Event{{Activity: "Reading the plan"}}))
	})
}

func TestAgentMapsEvents(t *testing.T) {
	tests := []struct {
		name string
		ev   chunkd.SessionLogEvent
		want plan.Event
	}{
		{
			name: "activity",
			ev:   chunkd.SessionLogEvent{Seq: 0, Kind: chunkd.SessionLogActivity, Text: "Edit plan"},
			want: plan.Event{Activity: "Edit plan"},
		},
		{
			name: "reply",
			ev:   chunkd.SessionLogEvent{Seq: 0, Kind: chunkd.SessionLogReply, Text: "Done.", ByTask: true},
			want: plan.Event{Reply: &plan.Reply{Text: "Done.", ByTask: true}},
		},
		{
			name: "problem",
			ev:   chunkd.SessionLogEvent{Seq: 0, Kind: chunkd.SessionLogProblem, Text: "claude exited"},
			want: plan.Event{Problem: errors.New("claude exited")},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				d := newFakeDaemon(
					answer{log: logOf("log-1", 0, 0)},
					answer{log: logOf("log-1", 0, 1, tt.ev)},
				)
				a, _ := newTestAgent(t, "plan-1", d)

				got := collect(t, a, d, 3)
				assert.Assert(t, cmp.Len(got, 1))
				ev := got[0]
				assert.Check(t, cmp.Equal(ev.Activity, tt.want.Activity))
				assert.Check(t, cmp.DeepEqual(ev.Reply, tt.want.Reply))
				if tt.want.Problem == nil {
					assert.Check(t, ev.Problem == nil)
				} else {
					assert.Check(t, cmp.Error(ev.Problem, tt.want.Problem.Error()))
				}
			})
		})
	}
}

func TestAgentEmitsEventsInOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := newFakeDaemon(
			answer{log: logOf("log-1", 0, 0)},
			answer{log: logOf("log-1", 0, 3,
				activity(0, "one"),
				activity(1, "two"),
				chunkd.SessionLogEvent{Seq: 2, Kind: chunkd.SessionLogReply, Text: "three"},
			)},
		)
		a, _ := newTestAgent(t, "plan-1", d)

		got := collect(t, a, d, 3)
		assert.Check(t, cmp.DeepEqual(got, []plan.Event{
			{Activity: "one"},
			{Activity: "two"},
			{Reply: &plan.Reply{Text: "three"}},
		}))
		assert.Check(t, cmp.DeepEqual(d.fetched()[:3], []int{-1, 0, 3}))
	})
}

// A new log ID means the daemon restarted, or the planner was made again:
// the cursor is reset to the new log's oldest event, so a screen polling at
// seq 57 of the old log still sees the new one's seq 3.
func TestAgentReadsANewLogFromItsOldest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := newFakeDaemon(
			answer{log: logOf("log-1", 0, 57)},
			answer{log: logOf("log-2", 1, 4)},
			answer{log: logOf("log-2", 1, 4, activity(1, "one"), activity(2, "two"), activity(3, "three"))},
		)
		a, _ := newTestAgent(t, "plan-1", d)

		got := collect(t, a, d, 4)
		assert.Check(t, cmp.DeepEqual(d.fetched()[:4], []int{-1, 57, 1, 4}))
		assert.Check(t, cmp.DeepEqual(got, []plan.Event{{Activity: "one"}, {Activity: "two"}, {Activity: "three"}}))
	})
}

func TestAgentShowsAPlannerMidTurnWorking(t *testing.T) {
	tests := []struct {
		name     string
		busy     bool
		activity string
		want     []plan.Event
	}{
		{
			name:     "busy with an activity",
			busy:     true,
			activity: "Read plan.md",
			want:     []plan.Event{{Activity: "Read plan.md"}},
		},
		{
			name: "busy before any activity",
			busy: true,
			want: []plan.Event{{Activity: "Working…"}},
		},
		{
			name: "idle",
			busy: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				first := logOf("log-1", 0, 2)
				first.Busy, first.Activity = tt.busy, tt.activity
				// A later fetch's busy says nothing on its own.
				later := logOf("log-1", 0, 2)
				later.Busy, later.Activity = true, "Edit plan"
				d := newFakeDaemon(answer{log: first}, answer{log: later})
				a, _ := newTestAgent(t, "plan-1", d)

				got := collect(t, a, d, 4)
				assert.Check(t, cmp.DeepEqual(got, tt.want))
			})
		})
	}
}

func TestAgentWarnsOnceWhileTheDaemonIsUnreachable(t *testing.T) {
	down := answer{err: fmt.Errorf("%w: connection refused", chunkd.ErrDaemonUnavailable)}
	busy := logOf("log-1", 0, 5)
	busy.Busy = true
	tests := []struct {
		name    string
		answers []answer
		// afters is the cursor of each fetch.
		afters []int
		want   []plan.Event
	}{
		{
			name:    "from the first fetch",
			answers: []answer{down, down, down, down, {log: busy}},
			afters:  []int{-1, -1, -1, -1, -1, 5},
			want:    []plan.Event{{Problem: errUnreachable}, {Activity: "Working…"}},
		},
		{
			name:    "after a fetch",
			answers: []answer{{log: logOf("log-1", 0, 2)}, down, down, down, {log: logOf("log-1", 0, 2)}},
			afters:  []int{-1, 2, 2, 2, 2, 2},
			want:    []plan.Event{{Problem: errUnreachable}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				d := newFakeDaemon(tt.answers...)
				a, _ := newTestAgent(t, "plan-1", d)

				got := collect(t, a, d, len(tt.afters))
				assert.Check(t, cmp.DeepEqual(d.fetched()[:len(tt.afters)], tt.afters))
				problems := 0
				for _, ev := range got {
					if ev.Problem != nil {
						problems++
					}
				}
				assert.Check(t, cmp.Equal(problems, 1))
				assert.Check(t, cmp.Len(got, len(tt.want)))
				for i := range min(len(got), len(tt.want)) {
					assert.Check(t, cmp.Equal(got[i].Activity, tt.want[i].Activity))
					assert.Check(t, cmp.Equal(got[i].Problem, tt.want[i].Problem))
				}
			})
		})
	}
}

// Once a fetch has succeeded, the next failure warns again.
func TestAgentWarnsAgainAfterTheDaemonCameBack(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		down := answer{err: fmt.Errorf("%w: connection refused", chunkd.ErrDaemonUnavailable)}
		d := newFakeDaemon(answer{log: logOf("log-1", 0, 0)}, down, down, answer{log: logOf("log-1", 0, 0)}, down, down)
		a, _ := newTestAgent(t, "plan-1", d)

		got := collect(t, a, d, 7)
		assert.Assert(t, cmp.Len(got, 2))
		for _, ev := range got {
			assert.Check(t, cmp.Equal(ev.Problem, errUnreachable))
		}
	})
}

func TestAgentStopEndsPollingAndSendsNothing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := newFakeDaemon()
		a, _ := newTestAgent(t, "plan-1", d)
		collect(t, a, d, 2)

		err := a.Stop()
		assert.NilError(t, err)
		// A fetch already under way may finish; none starts after it.
		synctest.Wait()
		n := len(d.fetched())
		time.Sleep(20 * pollInterval)
		synctest.Wait()
		assert.Check(t, cmp.Len(d.fetched(), n))
		assert.Check(t, cmp.Equal(d.posts(), 0))
	})
}

// A screen that stops reading does not hold up the poller once it is
// stopped.
func TestAgentStopEndsAnEmitNobodyReads(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := newFakeDaemon(
			answer{log: logOf("log-1", 0, 0)},
			answer{log: logOf("log-1", 0, 2, activity(0, "one"), activity(1, "two"))},
		)
		a, _ := newTestAgent(t, "plan-1", d)
		for len(d.fetched()) < 2 {
			<-d.fetchedCh
		}
		err := a.Stop()
		assert.NilError(t, err)
		// The bubble ends only once the poller has returned, so an emit
		// blocked past Stop fails the test as a deadlock.
		time.Sleep(20 * pollInterval)
		synctest.Wait()
		assert.Check(t, cmp.Len(d.fetched(), 2))
	})
}

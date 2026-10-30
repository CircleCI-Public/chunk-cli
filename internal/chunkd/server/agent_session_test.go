package server

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/claudecode"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
)

// reply ends the session's turn with text, as claude would.
func (b *fakeBuild) reply(text string) {
	b.events <- factory.AgentEvent{TurnEnded: &factory.AgentTurn{Reply: text}}
}

// act has the session report an activity.
func (b *fakeBuild) act(detail string) {
	b.events <- factory.AgentEvent{Activity: &claudecode.Activity{Tool: "Bash", Detail: detail}}
}

// fakeStarts starts fakeBuild sessions, recording each start's config and
// first message.
type fakeStarts struct {
	mu        sync.Mutex
	cfgs      []factory.AgentConfig
	firsts    []string
	builds    []*fakeBuild
	startErr  error
	resumeErr error // fails a start that resumes
}

func (f *fakeStarts) start(_ context.Context, cfg factory.AgentConfig, message string) (AgentSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.startErr != nil {
		return nil, f.startErr
	}
	f.cfgs = append(f.cfgs, cfg)
	if cfg.Resume && f.resumeErr != nil {
		return nil, f.resumeErr
	}
	b := newFakeBuild()
	f.builds = append(f.builds, b)
	f.firsts = append(f.firsts, message)
	return b, nil
}

func (f *fakeStarts) configs() []factory.AgentConfig {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]factory.AgentConfig(nil), f.cfgs...)
}

func (f *fakeStarts) session(i int) *fakeBuild {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.builds[i]
}

func (f *fakeStarts) first(i int) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.firsts[i]
}

func (f *fakeStarts) sessions() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.builds)
}

func (f *fakeStarts) setErrs(start, resume error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.startErr, f.resumeErr = start, resume
}

// memRecord is a session record in memory: id is the session it holds, and
// held fails opening it, as another chunk holding it open would.
type memRecord struct {
	mu       sync.Mutex
	id       string
	recorded []string
	closed   bool
	held     bool
	opens    int
}

func (r *memRecord) ID() (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.id, nil
}

func (r *memRecord) Record(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.id = id
	r.recorded = append(r.recorded, id)
	return nil
}

func (r *memRecord) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	return nil
}

func (r *memRecord) open() (SessionRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.held {
		return nil, factory.ErrPlanSessionOpen
	}
	r.opens++
	r.closed = false
	return r, nil
}

func (r *memRecord) recordedIDs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.recorded...)
}

func (r *memRecord) wasClosed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closed
}

func newTestRunner(t *testing.T) (*runner, *fakeStarts) {
	t.Helper()
	fs := &fakeStarts{}
	r := newRunner(t.Context(), factory.AgentConfig{Model: "sonnet"}, fs.start)
	t.Cleanup(r.stop)
	return r, fs
}

func inDir() (string, error) { return "/data/plans/plan-1", nil }

// waitEnded waits for r's session's events to end.
func waitEnded(t *testing.T, r *runner) {
	t.Helper()
	waitUntil(t, "the session to end", func() bool {
		_, live := r.live()
		return !live
	})
}

// waitNext waits for r's log to hold n events.
func waitNext(t *testing.T, r *runner, n int) chunkd.SessionLog {
	t.Helper()
	waitUntil(t, fmt.Sprintf("%d events", n), func() bool { return r.view(-1).Next >= n })
	return r.view(0)
}

func TestRunnerStartsUnderItsOwnIDAndRecordsIt(t *testing.T) {
	r, fs := newTestRunner(t)
	rec := &memRecord{}
	resumable, err := r.open(rec)
	assert.NilError(t, err)
	assert.Check(t, !resumable)

	started, err := r.send("hello", inDir)
	assert.NilError(t, err)
	assert.Check(t, started)
	cfg := fs.configs()[0]
	assert.Check(t, cfg.SessionID != "")
	assert.Check(t, cmp.Equal(cfg.SessionID, r.sessionID()))
	assert.Check(t, !cfg.Resume)
	assert.Check(t, cmp.Equal(cfg.Dir, "/data/plans/plan-1"))
	assert.Check(t, cmp.Equal(cfg.Model, "sonnet"))
	assert.Check(t, cmp.Equal(fs.first(0), "hello"))
	assert.Check(t, cmp.DeepEqual(rec.recordedIDs(), []string{cfg.SessionID}))

	// A live session is sent to, not started again.
	started, err = r.send("more", inDir)
	assert.NilError(t, err)
	assert.Check(t, !started)
	assert.Check(t, cmp.DeepEqual(fs.session(0).messages(), []string{"more"}))
}

func TestRunnerResumesTheRecordedSession(t *testing.T) {
	r, fs := newTestRunner(t)
	resumable, err := r.open(&memRecord{id: "s-0"})
	assert.NilError(t, err)
	assert.Check(t, resumable)

	_, err = r.send("hello", inDir)
	assert.NilError(t, err)
	cfg := fs.configs()[0]
	assert.Check(t, cfg.Resume)
	assert.Check(t, cmp.Equal(cfg.SessionID, "s-0"))
	assert.Check(t, cmp.Equal(r.view(-1).SessionID, "s-0"))
}

// claude may no longer have a recorded session's conversation; the session
// then starts afresh under the recorded ID.
func TestRunnerStartsTheRecordedSessionAfreshWhenItCannotResume(t *testing.T) {
	r, fs := newTestRunner(t)
	fs.setErrs(nil, errors.New("claude exited before it started: No conversation found"))
	rec := &memRecord{id: "s-0"}
	_, err := r.open(rec)
	assert.NilError(t, err)

	started, err := r.send("hello", inDir)
	assert.NilError(t, err)
	assert.Check(t, started)
	cfgs := fs.configs()
	assert.Assert(t, cmp.Len(cfgs, 2))
	assert.Check(t, cfgs[0].Resume)
	assert.Check(t, !cfgs[1].Resume)
	assert.Check(t, cmp.Equal(cfgs[1].SessionID, "s-0"))
	assert.Check(t, cmp.DeepEqual(rec.recordedIDs(), []string{"s-0"}))
}

func TestRunnerResumesASessionThatEnded(t *testing.T) {
	r, fs := newTestRunner(t)
	_, err := r.send("hello", inDir)
	assert.NilError(t, err)
	fs.session(0).end()
	waitEnded(t, r)

	started, err := r.send("again", inDir)
	assert.NilError(t, err)
	assert.Check(t, started)
	cfgs := fs.configs()
	assert.Assert(t, cmp.Len(cfgs, 2))
	assert.Check(t, cfgs[1].Resume)
	assert.Check(t, cmp.Equal(cfgs[1].SessionID, cfgs[0].SessionID))
	assert.Check(t, cmp.Equal(fs.first(1), "again"))
}

func TestRunnerStartErrorComesBackAsIs(t *testing.T) {
	r, fs := newTestRunner(t)
	fs.setErrs(factory.ErrNoFactoryPlugin, nil)
	_, err := r.send("hello", inDir)
	assert.Check(t, cmp.ErrorIs(err, factory.ErrNoFactoryPlugin))
	_, live := r.live()
	assert.Check(t, !live)

	_, err = r.send("hello", func() (string, error) { return "", errors.New("no origin") })
	assert.Check(t, cmp.ErrorContains(err, "no origin"))
}

func TestRunnerLogPages(t *testing.T) {
	for _, tc := range []struct {
		name       string
		appends    int
		after      int
		wantSeqs   []int // nil for none
		wantFirst  int   // with wantLen, for long pages
		wantLen    int
		wantOldest int
	}{
		{
			name:       "no after",
			appends:    3,
			after:      -1,
			wantOldest: 0,
		},
		{
			name:     "after mid",
			appends:  5,
			after:    2,
			wantSeqs: []int{2, 3, 4},
		},
		{
			name:    "after the end",
			appends: 5,
			after:   5,
		},
		{
			name:       "after below oldest after 600 appends",
			appends:    600,
			after:      50,
			wantFirst:  100,
			wantLen:    500,
			wantOldest: 100,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newTestRunner(t)
			for i := range tc.appends {
				r.note(chunkd.SessionLogProblem, fmt.Sprintf("event %d", i))
			}
			v := r.view(tc.after)
			assert.Check(t, cmp.Equal(v.Next, tc.appends))
			assert.Check(t, cmp.Equal(v.Oldest, tc.wantOldest))
			assert.Check(t, v.LogID != "")
			assert.Check(t, v.Events != nil, "events must encode as [], not null")
			if tc.wantLen > 0 {
				assert.Assert(t, cmp.Len(v.Events, tc.wantLen))
				assert.Check(t, cmp.Equal(v.Events[0].Seq, tc.wantFirst))
				assert.Check(t, cmp.Equal(v.Events[0].Text, fmt.Sprintf("event %d", tc.wantFirst)))
				return
			}
			var seqs []int
			for _, ev := range v.Events {
				seqs = append(seqs, ev.Seq)
			}
			assert.Check(t, cmp.DeepEqual(seqs, tc.wantSeqs))
		})
	}
	t.Run("a new runner has a new log_id", func(t *testing.T) {
		a, _ := newTestRunner(t)
		b, _ := newTestRunner(t)
		assert.Check(t, a.view(-1).LogID != b.view(-1).LogID)
	})
}

func TestRunnerLogsWhatTheSessionDoes(t *testing.T) {
	r, fs := newTestRunner(t)
	_, err := r.send("hello", inDir)
	assert.NilError(t, err)
	v := r.view(-1)
	assert.Check(t, v.Busy, "a send makes the session busy")
	assert.Check(t, cmp.Equal(v.Activity, ""))

	s := fs.session(0)
	s.act("factory daemon wait")
	waitNext(t, r, 1)
	v = r.view(-1)
	line := (claudecode.Activity{Tool: "Bash", Detail: "factory daemon wait"}).Line()
	assert.Check(t, v.Busy)
	assert.Check(t, cmp.Equal(v.Activity, line))

	s.events <- factory.AgentEvent{TurnEnded: &factory.AgentTurn{Reply: "Did it.", ByTask: true}}
	waitNext(t, r, 2)
	waitUntil(t, "a reply to end the turn", func() bool { return !r.view(-1).Busy })
	assert.Check(t, cmp.Equal(r.view(-1).Activity, ""))

	s.act("step")
	s.events <- factory.AgentEvent{Problem: errors.New("claude exited with code 1")}
	v = waitNext(t, r, 4)
	assert.Check(t, !v.Busy, "a problem ends the turn")
	assert.Check(t, cmp.DeepEqual(v.Events, []chunkd.SessionLogEvent{
		{Seq: 0, Kind: chunkd.SessionLogActivity, Text: line},
		{Seq: 1, Kind: chunkd.SessionLogReply, Text: "Did it.", ByTask: true},
		{Seq: 2, Kind: chunkd.SessionLogActivity, Text: (claudecode.Activity{Tool: "Bash", Detail: "step"}).Line()},
		{Seq: 3, Kind: chunkd.SessionLogProblem, Text: "claude exited with code 1"},
	}))

	// A send to the idle session makes it busy again.
	_, err = r.send("more", inDir)
	assert.NilError(t, err)
	assert.Check(t, r.view(-1).Busy)
}

func TestRunnerOnReplyFollowsTheReply(t *testing.T) {
	r, fs := newTestRunner(t)
	var mu sync.Mutex
	var seen []bool
	r.onReply = func(byTask bool) {
		mu.Lock()
		seen = append(seen, byTask)
		mu.Unlock()
		// The reply is in the log by now.
		v := r.view(0)
		r.note(chunkd.SessionLogProblem, fmt.Sprintf("after %d", len(v.Events)))
	}
	_, err := r.send("hello", inDir)
	assert.NilError(t, err)
	fs.session(0).reply("Claimed.")
	v := waitNext(t, r, 2)
	assert.Check(t, cmp.DeepEqual(v.Events, []chunkd.SessionLogEvent{
		{Seq: 0, Kind: chunkd.SessionLogReply, Text: "Claimed."},
		{Seq: 1, Kind: chunkd.SessionLogProblem, Text: "after 1"},
	}))
	mu.Lock()
	defer mu.Unlock()
	assert.Check(t, cmp.DeepEqual(seen, []bool{false}))
}

// Two clients reading the log at once each see every event.
func TestRunnerLogHasManyReaders(t *testing.T) {
	r, fs := newTestRunner(t)
	_, err := r.send("hello", inDir)
	assert.NilError(t, err)
	const n = 50
	var wg sync.WaitGroup
	got := make([][]string, 2)
	for i := range got {
		wg.Go(func() {
			cursor := 0
			for len(got[i]) < n {
				v := r.view(cursor)
				for _, ev := range v.Events {
					got[i] = append(got[i], ev.Text)
				}
				cursor = v.Next
			}
		})
	}
	s := fs.session(0)
	var want []string
	for i := range n {
		text := fmt.Sprintf("reply %d", i)
		want = append(want, text)
		s.reply(text)
	}
	wg.Wait()
	assert.Check(t, cmp.DeepEqual(got[0], want))
	assert.Check(t, cmp.DeepEqual(got[1], want))
}

// lateBuild is a session that goes on reporting events after Stop, until it
// is ended.
type lateBuild struct{ *fakeBuild }

func (b lateBuild) Stop() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.stopped = true
	return nil
}

func TestRunnerStopStopsTheSessionAndDropsItsLateEvents(t *testing.T) {
	late := lateBuild{newFakeBuild()}
	r := newRunner(t.Context(), factory.AgentConfig{}, func(context.Context, factory.AgentConfig, string) (AgentSession, error) {
		return late, nil
	})
	rec := &memRecord{}
	_, err := r.open(rec)
	assert.NilError(t, err)
	_, err = r.send("hello", inDir)
	assert.NilError(t, err)

	stopped := make(chan struct{})
	go func() {
		r.stop()
		close(stopped)
	}()
	waitUntil(t, "the session to be stopped", late.wasStopped)
	late.reply("too late")
	select {
	case <-stopped:
		t.Fatal("stop returned before the session's events ended")
	default:
	}
	late.end()
	<-stopped

	assert.Check(t, rec.wasClosed(), "the record was left open")
	v := r.view(0)
	assert.Check(t, cmp.Len(v.Events, 0), "a stopped session's late event was logged")
	assert.Check(t, !v.Busy)
	_, err = r.send("more", inDir)
	assert.Check(t, cmp.ErrorIs(err, errRunnerStopped))

	r.stop() // again: nothing to do
}

func TestSessionStartProblem(t *testing.T) {
	for _, tc := range []struct {
		err   error
		build string
		plan  string
	}{
		{
			err:   factory.ErrNoClaude,
			build: "the chunk daemon found no Claude Code to build with; install it from https://claude.com/claude-code",
			plan:  "the chunk daemon found no Claude Code to plan with; install it from https://claude.com/claude-code",
		},
		{
			err:   factory.ErrNoFactory,
			build: "the chunk daemon found no factory; install it and put it on PATH, then restart the daemon",
			plan:  "the chunk daemon found no factory; install it and put it on PATH, then restart the daemon",
		},
		{
			err:   fmt.Errorf("%w: install it", factory.ErrNoFactoryPlugin),
			build: "factory's f plugin is not loaded in Claude Code; install it",
			plan:  "factory's f plugin is not loaded in Claude Code; install it",
		},
		{
			err:   factory.ErrFactoryMCP,
			build: "factory's MCP server is not connected; make sure factory is on PATH",
			plan:  "factory's MCP server is not connected; make sure factory is on PATH",
		},
		{
			err:   factory.ErrNoFactoryDaemon,
			build: "the factory daemon is not running; run `factory daemon` with FACTORY_PLAN_STORE=circleci to plan",
			plan:  "the factory daemon is not running; run `factory daemon` with FACTORY_PLAN_STORE=circleci to plan",
		},
		{
			err:   errors.New("claude exited"),
			build: "start the build session: claude exited",
			plan:  "start the planning session: claude exited",
		},
	} {
		t.Run(tc.err.Error(), func(t *testing.T) {
			assert.Check(t, cmp.Error(sessionStartProblem(tc.err, "build"), tc.build))
			assert.Check(t, cmp.Error(sessionStartProblem(tc.err, "plan"), tc.plan))
		})
	}
}

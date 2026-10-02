package watchd

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"
)

// MaxSessionsPerProject caps retained sessions per project. Only finished ones
// are evicted, so a live session is never lost.
const MaxSessionsPerProject = 10

// maxReviewOutput caps the prose kept for one review. Claude's own output is
// already bounded by the review package; this is the daemon's separate bound on
// how much of it stays in memory per finished round.
const maxReviewOutput = 128 * 1024

// sessionEntry is one session and the machinery that owns it.
type sessionEntry struct {
	mu      sync.Mutex
	s       Session
	details []RoundDetail // parallel to s.Rounds
	cancel  context.CancelFunc
	// cancelled records that cancellation was asked for, so a session stopped by
	// it is reported as cancelled rather than as whatever error the stop made.
	cancelled bool
	done      chan struct{}
	// resume wakes a paused session. Buffered so a resume that arrives a moment
	// before the session starts waiting is not lost.
	resume chan struct{}
	// leftTree is the user's working tree as the session last left it, after the
	// most recent fix; loopNote is why the review loop ended, and loopFailed
	// that it ran to its end without its work passing. All are internal: the
	// record shows their consequences.
	leftTree   string
	loopNote   string
	loopFailed bool
	// sidecarReview maps a sandbox to the review currently running on it, so a
	// command submitted there can be attributed to its review.
	sidecarReview map[string]string
}

func cloneSession(s Session) Session {
	out := s
	out.PausedPaths = slices.Clone(s.PausedPaths)
	out.Stages = slices.Clone(s.Stages)
	out.Rounds = make([]Round, len(s.Rounds))
	for i, r := range s.Rounds {
		out.Rounds[i] = r
		out.Rounds[i].Reviews = slices.Clone(r.Reviews)
		if r.EndedAt != nil {
			ended := *r.EndedAt
			out.Rounds[i].EndedAt = &ended
		}
		out.Rounds[i].Checks = slices.Clone(r.Checks)
		if r.Implement != nil {
			impl := *r.Implement
			out.Rounds[i].Implement = &impl
		}
		if r.Fix != nil {
			fix := *r.Fix
			fix.Files = slices.Clone(r.Fix.Files)
			fix.FindingIDs = slices.Clone(r.Fix.FindingIDs)
			out.Rounds[i].Fix = &fix
		}
	}
	if s.Factory != nil {
		f := *s.Factory
		f.KeptSidecars = slices.Clone(s.Factory.KeptSidecars)
		f.Progress = s.Factory.Progress.clone()
		out.Factory = &f
	}
	if s.Restore != nil {
		rp := *s.Restore
		rp.Paths = slices.Clone(s.Restore.Paths)
		out.Restore = &rp
	}
	if s.EndedAt != nil {
		ended := *s.EndedAt
		out.EndedAt = &ended
	}
	return out
}

func (e *sessionEntry) snapshot() Session {
	e.mu.Lock()
	defer e.mu.Unlock()
	return cloneSession(e.s)
}

func (e *sessionEntry) detail() SessionDetail {
	e.mu.Lock()
	defer e.mu.Unlock()
	d := SessionDetail{Session: cloneSession(e.s), Details: make([]RoundDetail, len(e.details))}
	for i, rd := range e.details {
		d.Details[i] = RoundDetail{Number: rd.Number, Results: slices.Clone(rd.Results)}
	}
	return d
}

// reviewIndexLocked finds a review of a round by name, or -1.
func (e *sessionEntry) reviewIndexLocked(round int, name string) int {
	for i := range e.s.Rounds[round].Reviews {
		if e.s.Rounds[round].Reviews[i].Name == name {
			return i
		}
	}
	return -1
}

// stageLocked returns the stage with the given ID.
func (e *sessionEntry) stageLocked(id StageID) *Stage {
	for i := range e.s.Stages {
		if e.s.Stages[i].ID == id {
			return &e.s.Stages[i]
		}
	}
	return nil
}

// sessionStore holds every session. Like the task store it lives in memory only:
// a daemon restart forgets sessions. What a session changed in the user's files
// is not forgotten: the restore point is a git ref.
type sessionStore struct {
	parent context.Context

	mu        sync.Mutex
	sessions  map[string]*sessionEntry
	byProject map[string][]string // project root → session IDs, oldest first
}

func newSessionStore(parent context.Context) *sessionStore {
	if parent == nil {
		parent = context.Background()
	}
	return &sessionStore{
		parent:    parent,
		sessions:  make(map[string]*sessionEntry),
		byProject: make(map[string][]string),
	}
}

// add creates a running entry for s. If the project already has a session that
// has not ended — running, or paused and waiting — it creates nothing and says
// why. One at a time per project: two sessions would fight over the same files.
func (s *sessionStore) add(sess Session) (*sessionEntry, context.Context, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range s.byProject[sess.ProjectRoot] {
		if other := s.sessions[id]; other != nil && !other.snapshot().State.Finished() {
			return nil, nil, fmt.Sprintf("a session is already active for this project (session %s)", id)
		}
	}
	sess.ID = uuid.NewString()
	sess.State = SessionRunning
	sess.Stages = newStages(sess.loopStage())
	sess.StartedAt = time.Now()
	ctx, cancel := context.WithCancel(s.parent)
	entry := &sessionEntry{
		s:             sess,
		cancel:        cancel,
		done:          make(chan struct{}),
		resume:        make(chan struct{}, 1),
		sidecarReview: make(map[string]string),
	}
	s.sessions[sess.ID] = entry
	s.byProject[sess.ProjectRoot] = append(s.byProject[sess.ProjectRoot], sess.ID)
	s.evictLocked(sess.ProjectRoot)
	return entry, ctx, ""
}

// evictLocked drops the oldest finished sessions until the project is within
// the cap.
func (s *sessionStore) evictLocked(root string) {
	ids := s.byProject[root]
	for len(ids) > MaxSessionsPerProject {
		dropped := false
		for i, id := range ids {
			if e := s.sessions[id]; e != nil && !e.snapshot().State.Finished() {
				continue
			}
			delete(s.sessions, id)
			ids = slices.Delete(ids, i, i+1)
			dropped = true
			break
		}
		if !dropped {
			break
		}
	}
	s.byProject[root] = ids
}

func (s *sessionStore) get(id string) *sessionEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[id]
}

// forProject lists a project's sessions, newest first. Safe on a nil store.
func (s *sessionStore) forProject(root string) []Session {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	ids := slices.Clone(s.byProject[root])
	entries := make([]*sessionEntry, 0, len(ids))
	for _, id := range ids {
		if e := s.sessions[id]; e != nil {
			entries = append(entries, e)
		}
	}
	s.mu.Unlock()

	out := make([]Session, 0, len(entries))
	for i := len(entries) - 1; i >= 0; i-- {
		out = append(out, entries[i].snapshot())
	}
	return out
}

// cancelSession asks a session to stop. It reports whether the session exists and was
// still going; cancelling an ended session is not an error worth a status code.
func (s *sessionStore) cancelSession(id string) (found, wasActive bool) {
	e := s.get(id)
	if e == nil {
		return false, false
	}
	e.mu.Lock()
	active := !e.s.State.Finished()
	if active {
		e.cancelled = true
	}
	e.mu.Unlock()
	if active {
		e.cancel()
	}
	return true, active
}

// stopAll cancels every session in flight and waits for them to settle.
func (s *sessionStore) stopAll() {
	if s == nil {
		return
	}
	s.mu.Lock()
	entries := make([]*sessionEntry, 0, len(s.sessions))
	for _, e := range s.sessions {
		entries = append(entries, e)
	}
	s.mu.Unlock()
	for _, e := range entries {
		e.cancel()
	}
	for _, e := range entries {
		<-e.done
	}
}

// truncateOutput keeps at most maxReviewOutput bytes of a review's prose,
// cutting on a rune boundary and saying so.
func truncateOutput(s string) string {
	if len(s) <= maxReviewOutput {
		return s
	}
	cut := maxReviewOutput
	for cut > 0 && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return s[:cut] + "\n[review output truncated]"
}

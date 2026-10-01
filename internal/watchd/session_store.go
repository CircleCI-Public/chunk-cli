package watchd

import (
	"context"
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
	// loopNote is why the loop ended and outcome how; both go on the record once
	// the session settles.
	loopNote string
	outcome  Outcome
	// sidecarReview maps a sandbox to the review currently running on it, so a
	// command submitted there can be attributed to its review.
	sidecarReview map[string]string
	// worker is the sidecar the implementer works on, kept for every turn so its
	// Claude session, claudeSession, can be resumed there.
	worker        string
	claudeSession string
}

func cloneSession(s Session) Session {
	out := s
	out.Stages = slices.Clone(s.Stages)
	out.Rounds = make([]Round, len(s.Rounds))
	for i, r := range s.Rounds {
		out.Rounds[i] = r
		out.Rounds[i].Reviews = slices.Clone(r.Reviews)
		if r.EndedAt != nil {
			ended := *r.EndedAt
			out.Rounds[i].EndedAt = &ended
		}
		out.Rounds[i].Fix = cloneFix(r.Fix)
	}
	out.Implement = cloneFix(s.Implement)
	if s.EndedAt != nil {
		ended := *s.EndedAt
		out.EndedAt = &ended
	}
	return out
}

func cloneFix(f *RoundFix) *RoundFix {
	if f == nil {
		return nil
	}
	fix := *f
	fix.Files = slices.Clone(f.Files)
	fix.FindingIDs = slices.Clone(f.FindingIDs)
	return &fix
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
	return e.checkIndexLocked(round, "", name)
}

// checkIndexLocked finds a check of a round by kind and name, or -1. A review
// and a validation command may share a name.
func (e *sessionEntry) checkIndexLocked(round int, kind CheckKind, name string) int {
	for i, c := range e.s.Rounds[round].Reviews {
		if c.Kind == kind && c.Name == name {
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
// a daemon restart forgets sessions. Their work is not forgotten: it is on a
// branch.
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

// add creates a running entry for sess. Sessions do not conflict with each
// other or with the user: each works in a worktree of its own.
func (s *sessionStore) add(sess Session) (*sessionEntry, context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess.ID = uuid.NewString()
	sess.State = SessionRunning
	sess.Stages = newStages()
	sess.StartedAt = time.Now()
	ctx, cancel := context.WithCancel(s.parent)
	entry := &sessionEntry{
		s:             sess,
		cancel:        cancel,
		done:          make(chan struct{}),
		sidecarReview: make(map[string]string),
	}
	s.sessions[sess.ID] = entry
	s.byProject[sess.ProjectRoot] = append(s.byProject[sess.ProjectRoot], sess.ID)
	s.evictLocked(sess.ProjectRoot)
	return entry, ctx
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

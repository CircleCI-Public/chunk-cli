package server

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
)

// MaxSessionsPerProject caps retained sessions per project. Only finished ones
// are evicted, so a live session is never lost.
const MaxSessionsPerProject = 10

// sessionEntry is one session and the machinery that owns it.
type sessionEntry struct {
	mu     sync.Mutex
	s      chunkd.Session
	cancel context.CancelFunc
	// cancelled records that cancellation was asked for, so a session stopped by
	// it is reported as cancelled rather than as whatever error the stop made.
	cancelled bool
	done      chan struct{}
	// loopNote is why the session ended, when it ended by itself. It is
	// internal: the record shows it as its stage's note.
	loopNote string
}

func cloneSession(s chunkd.Session) chunkd.Session {
	out := s
	out.Stages = slices.Clone(s.Stages)
	if s.Factory != nil {
		f := *s.Factory
		f.Progress = cloneFeed(s.Factory.Progress)
		out.Factory = &f
	}
	if s.EndedAt != nil {
		ended := *s.EndedAt
		out.EndedAt = &ended
	}
	return out
}

func (e *sessionEntry) snapshot() chunkd.Session {
	e.mu.Lock()
	defer e.mu.Unlock()
	return cloneSession(e.s)
}

// stageLocked returns the stage with the given ID.
func (e *sessionEntry) stageLocked(id chunkd.StageID) *chunkd.Stage {
	for i := range e.s.Stages {
		if e.s.Stages[i].ID == id {
			return &e.s.Stages[i]
		}
	}
	return nil
}

// sessionStore holds every session. Like the task store it lives in memory only:
// a daemon restart forgets sessions. A plan build's work is not forgotten with
// it: it is in factory, and committed on the plan's branch.
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

// add creates a running entry for s. A project can have any number of
// sessions going at once.
func (s *sessionStore) add(sess chunkd.Session) (*sessionEntry, context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess.ID = uuid.NewString()
	sess.State = chunkd.SessionRunning
	sess.Stages = newStages()
	sess.StartedAt = time.Now()
	ctx, cancel := context.WithCancel(s.parent)
	entry := &sessionEntry{
		s:      sess,
		cancel: cancel,
		done:   make(chan struct{}),
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
func (s *sessionStore) forProject(root string) []chunkd.Session {
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

	out := make([]chunkd.Session, 0, len(entries))
	for i := len(entries) - 1; i >= 0; i-- {
		out = append(out, entries[i].snapshot())
	}
	return out
}

// entries lists every session the store holds. Safe on a nil store.
func (s *sessionStore) entries() []*sessionEntry {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*sessionEntry, 0, len(s.sessions))
	for _, e := range s.sessions {
		out = append(out, e)
	}
	return out
}

// cancelSession asks a session to stop. It reports whether the session exists;
// cancelling an ended session is not an error worth a status code.
func (s *sessionStore) cancelSession(id string) (found bool) {
	e := s.get(id)
	if e == nil {
		return false
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
	return true
}

// deleteSession forgets a finished session. It reports whether the session
// exists and whether it was removed: a session still going is left alone, since
// its loop owns it until it ends.
func (s *sessionStore) deleteSession(id string) (found, removed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.sessions[id]
	if e == nil {
		return false, false
	}
	snap := e.snapshot()
	if !snap.State.Finished() {
		return true, false
	}
	root := snap.ProjectRoot
	delete(s.sessions, id)
	if i := slices.Index(s.byProject[root], id); i >= 0 {
		s.byProject[root] = slices.Delete(s.byProject[root], i, i+1)
	}
	return true, true
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

// newStages returns the stages of a session that is starting: its loop,
// running.
func newStages() []chunkd.Stage {
	return []chunkd.Stage{{ID: chunkd.StageFactoryLoop, State: chunkd.StageRunning}}
}

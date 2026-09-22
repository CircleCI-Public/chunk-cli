package watchd

import (
	"strings"
	"sync"
	"time"
)

// claimStore tracks which sessions are actively validating which paths.
// Claims are advisory and in-memory; they are lost on daemon restart.
type claimStore struct {
	mu     sync.Mutex
	claims map[string]*ClaimState // key: sessionID+":"+canonRoot
	ttl    time.Duration
	now    func() time.Time
}

func newClaimStore() *claimStore {
	return &claimStore{
		claims: make(map[string]*ClaimState),
		ttl:    30 * time.Minute,
		now:    time.Now,
	}
}

func claimKey(sessionID, canonRoot string) string {
	return sessionID + ":" + canonRoot
}

// register upserts a claim for sessionID in root with the given paths.
// Passing nil paths means "working in this project, files unknown".
// A no-op when sessionID is empty.
func (s *claimStore) register(sessionID, root string, paths []string) {
	if sessionID == "" {
		return
	}
	cr := canonicalRoot(root)
	key := claimKey(sessionID, cr)
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.claims[key] = &ClaimState{
		SessionID:   sessionID,
		ProjectRoot: cr,
		Paths:       paths,
		ClaimedAt:   now,
		ExpiresAt:   now.Add(s.ttl),
	}
}

// release removes the claim for sessionID in root. Safe to call on unknown keys.
func (s *claimStore) release(sessionID, root string) {
	if sessionID == "" {
		return
	}
	key := claimKey(sessionID, canonicalRoot(root))
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.claims, key)
}

// overlapping returns non-expired claims from other sessions for root whose
// paths intersect with paths. If either side has nil/empty paths it is treated
// as a full-project overlap (conservative: returns the claim).
func (s *claimStore) overlapping(sessionID, root string, paths []string) []ClaimState {
	cr := canonicalRoot(root)
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []ClaimState
	for _, c := range s.claims {
		if c.SessionID == sessionID {
			continue
		}
		if c.ProjectRoot != cr {
			continue
		}
		if now.After(c.ExpiresAt) {
			continue
		}
		if pathsOverlap(paths, c.Paths) {
			out = append(out, *c)
		}
	}
	return out
}

// forProject returns all non-expired claims for root, including the caller's own.
// Used to populate ProjectSnapshot.ActiveClaims.
func (s *claimStore) forProject(root string) []ClaimState {
	cr := canonicalRoot(root)
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []ClaimState
	for _, c := range s.claims {
		if c.ProjectRoot != cr {
			continue
		}
		if now.After(c.ExpiresAt) {
			continue
		}
		out = append(out, *c)
	}
	return out
}

// expireAll removes claims whose TTL has elapsed. Called from the poll loop.
func (s *claimStore) expireAll() {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, c := range s.claims {
		if now.After(c.ExpiresAt) {
			delete(s.claims, key)
		}
	}
}

// pathsOverlap reports whether slices a and b share at least one element.
// Returns true when either side is nil or empty — a session whose changed
// files could not be determined is treated as potentially overlapping anything.
func pathsOverlap(a, b []string) bool {
	if len(a) == 0 || len(b) == 0 {
		return true
	}
	set := make(map[string]struct{}, len(a))
	for _, p := range a {
		set[strings.TrimPrefix(p, "./")] = struct{}{}
	}
	for _, p := range b {
		if _, ok := set[strings.TrimPrefix(p, "./")]; ok {
			return true
		}
	}
	return false
}

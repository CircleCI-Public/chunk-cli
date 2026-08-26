package daemon

import (
	"sync"
	"time"
)

// SyncState represents the sync status of a sidecar relative to the local repo.
type SyncState string

// Sync state values reported by the daemon.
const (
	SyncStateInSync    SyncState = "in_sync"
	SyncStateNeedsSync SyncState = "needs_sync"
	SyncStateNotSynced SyncState = "not_synced"
)

// SidecarState holds the current known state of a sidecar.
type SidecarState struct {
	ID                 string    `json:"id"`
	Name               string    `json:"name,omitempty"`
	SyncState          SyncState `json:"sync_state"`
	LastSyncedRef      string    `json:"last_synced_ref,omitempty"`
	ActiveInvocationID string    `json:"active_invocation_id,omitempty"`
}

// InvocationState is an in-flight validate/sync/hook run.
type InvocationState struct {
	ID        string    `json:"id"`
	SidecarID string    `json:"sidecar_id"`
	Op        string    `json:"op"`
	Branch    string    `json:"branch,omitempty"`
	StartedAt time.Time `json:"started_at"`
}

// InvocationSummary is the completed record of a run.
type InvocationSummary struct {
	ID         string    `json:"id"`
	SidecarID  string    `json:"sidecar_id"`
	Op         string    `json:"op"`
	Passed     int       `json:"passed"`
	Total      int       `json:"total"`
	DurationMs int64     `json:"duration_ms"`
	OK         bool      `json:"ok"`
	Msg        string    `json:"msg"`
	FinishedAt time.Time `json:"finished_at"`
}

// Snapshot is the full current state sent to new SSE subscribers.
type Snapshot struct {
	Sidecars map[string]*SidecarState `json:"sidecars"`
	History  []*InvocationSummary     `json:"recent_invocations"`
}

type state struct {
	mu       sync.RWMutex
	sidecars map[string]*SidecarState
	active   map[string]*InvocationState
	history  []*InvocationSummary
}

func newState() *state {
	return &state{
		sidecars: make(map[string]*SidecarState),
		active:   make(map[string]*InvocationState),
	}
}

func (s *state) upsertSidecar(sc SidecarState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sidecars[sc.ID] = &sc
}

func (s *state) startInvocation(inv InvocationState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active[inv.ID] = &inv
	if sc := s.sidecars[inv.SidecarID]; sc != nil {
		sc.ActiveInvocationID = inv.ID
	}
}

func (s *state) finishInvocation(sum *InvocationSummary) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if inv, ok := s.active[sum.ID]; ok {
		sum.SidecarID = inv.SidecarID
		sum.Op = inv.Op
		if sc := s.sidecars[inv.SidecarID]; sc != nil && sc.ActiveInvocationID == sum.ID {
			sc.ActiveInvocationID = ""
		}
	}
	delete(s.active, sum.ID)
	s.history = append([]*InvocationSummary{sum}, s.history...)
	const maxHistory = 20
	if len(s.history) > maxHistory {
		s.history = s.history[:maxHistory]
	}
}

func (s *state) snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sidecars := make(map[string]*SidecarState, len(s.sidecars))
	for k, v := range s.sidecars {
		cp := *v
		sidecars[k] = &cp
	}
	history := make([]*InvocationSummary, len(s.history))
	copy(history, s.history)
	return Snapshot{Sidecars: sidecars, History: history}
}

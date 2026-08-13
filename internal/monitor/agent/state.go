package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/monitor/ipc"
)

type state struct {
	Sessions map[string]ipc.Session `json:"sessions"`
}

type stateFile struct {
	mu   sync.Mutex
	path string
}

func newStateFile(path string) *stateFile {
	return &stateFile{path: path}
}

func (sf *stateFile) update(req ipc.Request) error {
	sf.mu.Lock()
	defer sf.mu.Unlock()

	s := sf.readLocked()

	at := req.Timestamp
	if at.IsZero() {
		at = time.Now()
	}

	sess, ok := s.Sessions[req.SessionID]
	if !ok {
		sess = ipc.Session{
			ID:         req.SessionID,
			ProjectDir: req.ProjectDir,
			StartedAt:  at,
			Status:     "active",
		}
	}
	sess.LastSeenAt = at
	if req.ProjectDir != "" {
		sess.ProjectDir = req.ProjectDir
	}
	if req.EventType == ipc.EventToolUse {
		sess.ToolUseCount++
	}
	if req.EventType == ipc.EventSessionEnd {
		sess.Status = "ended"
	}
	s.Sessions[req.SessionID] = sess

	return sf.writeLocked(s)
}

func (sf *stateFile) setValidation(sessionID, status string) error {
	sf.mu.Lock()
	defer sf.mu.Unlock()

	s := sf.readLocked()
	sess := s.Sessions[sessionID]
	sess.ValidationStatus = status
	s.Sessions[sessionID] = sess
	return sf.writeLocked(s)
}

func (sf *stateFile) list() ([]ipc.Session, error) {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	s := sf.readLocked()
	sessions := make([]ipc.Session, 0, len(s.Sessions))
	for _, sess := range s.Sessions {
		sessions = append(sessions, sess)
	}
	return sessions, nil
}

func (sf *stateFile) readLocked() state {
	data, err := os.ReadFile(sf.path)
	if err != nil {
		return state{Sessions: make(map[string]ipc.Session)}
	}
	var s state
	if err := json.Unmarshal(data, &s); err != nil || s.Sessions == nil {
		return state{Sessions: make(map[string]ipc.Session)}
	}
	return s
}

func (sf *stateFile) writeLocked(s state) error {
	out, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}
	return os.WriteFile(sf.path, out, 0o600)
}

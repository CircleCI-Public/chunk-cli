package eventlog

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
)

// Op identifies the kind of operation that generated an event.
type Op string

// Op values for each sidecar operation type.
const (
	OpSync     Op = "sync"
	OpValidate Op = "validate"
	OpExec     Op = "exec"
	OpSetup    Op = "setup"
)

// Event is a single status event written to the log.
type Event struct {
	Ts          time.Time `json:"ts"`
	SidecarID   string    `json:"sidecar_id,omitempty"`
	SidecarName string    `json:"sidecar_name,omitempty"`
	Branch      string    `json:"branch,omitempty"`
	Op          Op        `json:"op"`
	Level       string    `json:"level"` // "step", "info", "warn", "done"
	Msg         string    `json:"msg"`
}

const logFile = "events.jsonl"

// Log appends events to a JSONL file in a project data directory.
type Log struct {
	mu   sync.Mutex
	path string
}

// Open opens (or creates) the event log in dataDir.
func Open(dataDir string) (*Log, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	return &Log{path: filepath.Join(dataDir, logFile)}, nil
}

// Append writes a single event to the log.
func (l *Log) Append(e Event) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	encErr := json.NewEncoder(f).Encode(e)
	return errors.Join(encErr, f.Close())
}

// Wrap returns a StatusFunc that calls inner and also appends each call to the
// log. All events will be tagged with op, sidecarID, sidecarName, and branch.
func (l *Log) Wrap(inner iostream.StatusFunc, op Op, sidecarID, sidecarName, branch string) iostream.StatusFunc {
	return func(level iostream.Level, msg string) {
		if inner != nil {
			inner(level, msg)
		}
		_ = l.Append(Event{
			Ts:          time.Now(),
			SidecarID:   sidecarID,
			SidecarName: sidecarName,
			Branch:      branch,
			Op:          op,
			Level:       levelStr(level),
			Msg:         msg,
		})
	}
}

// Recent returns up to n most recent events from the log.
func (l *Log) Recent(n int) ([]Event, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := os.Open(l.path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var all []Event
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e Event
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			all = append(all, e)
		}
	}
	if err := sc.Err(); err != nil {
		return all, err
	}
	if len(all) <= n {
		return all, nil
	}
	return all[len(all)-n:], nil
}

// TailFrom returns events appended after offset, and the updated offset.
func (l *Log) TailFrom(offset int64) ([]Event, int64, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := os.Open(l.path)
	if os.IsNotExist(err) {
		return nil, offset, nil
	}
	if err != nil {
		return nil, offset, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, offset, err
	}
	if info.Size() <= offset {
		return nil, offset, nil
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, offset, err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, offset, err
	}
	newOffset := offset + int64(len(data))
	sc := bufio.NewScanner(bytes.NewReader(data))
	var events []Event
	for sc.Scan() {
		var e Event
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			events = append(events, e)
		}
	}
	return events, newOffset, sc.Err()
}

func levelStr(l iostream.Level) string {
	switch l {
	case iostream.LevelStep:
		return "step"
	case iostream.LevelInfo:
		return "info"
	case iostream.LevelWarn:
		return "warn"
	case iostream.LevelDone:
		return "done"
	case iostream.LevelError:
		return "error"
	default:
		return "info"
	}
}

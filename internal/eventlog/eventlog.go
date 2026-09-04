package eventlog

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
	OpHook     Op = "hook"
)

// Event is a single status event written to the log.
type Event struct {
	Ts          time.Time `json:"ts"`
	SidecarID   string    `json:"sidecar_id,omitempty"`
	SidecarName string    `json:"sidecar_name,omitempty"`
	Branch      string    `json:"branch,omitempty"`
	Op          Op        `json:"op"`
	Level       string    `json:"level"` // "step", "info", "warn", "done", "error"
	Msg         string    `json:"msg"`

	// Final marks the one event that closes an operation, with Passed and Total
	// carrying its tally. A reader tells a finished operation from one still in
	// flight by this, never by reading Msg.
	Final  bool `json:"final,omitempty"`
	Passed int  `json:"passed,omitempty"`
	Total  int  `json:"total,omitempty"`

	// CommandID is the sandbox-provisioner ID of the remote exec that produced
	// this event, set on pass/fail events for commands that ran on the sidecar.
	// Use GET /api/v3/sidecar/commands/{id}/output to replay the run's output.
	CommandID string `json:"command_id,omitempty"`
}

// Outcome reports whether e is the event that closes an operation, and the
// tally to show for it.
func (e Event) Outcome() (passed, total int, ok bool) {
	if e.Final {
		return e.Passed, e.Total, true
	}
	return legacyOutcome(e)
}

// legacyOutcome recognises the closing events written before Final existed,
// which carried the tally in the message as "N/M passed  Xs". Logs are trimmed
// as they grow, so this can go once the existing ones have rolled over.
func legacyOutcome(e Event) (int, int, bool) {
	if e.Op != OpValidate || (e.Level != levelDone && e.Level != levelError) {
		return 0, 0, false
	}
	first, _, _ := strings.Cut(e.Msg, " ")
	p, t, found := strings.Cut(first, "/")
	if !found {
		return 0, 0, false
	}
	passed, passedErr := strconv.Atoi(p)
	total, totalErr := strconv.Atoi(t)
	if passedErr != nil || totalErr != nil {
		return 0, 0, false
	}
	return passed, total, true
}

const logFile = "events.jsonl"

const (
	levelStep  = "step"
	levelInfo  = "info"
	levelWarn  = "warn"
	levelDone  = "done"
	levelError = "error"
)

// maxLogLines triggers a rotation; trimToLines is how many are kept.
const (
	maxLogLines = 2000
	trimToLines = 500
)

// Log appends events to a JSONL file in a project data directory.
type Log struct {
	mu   sync.Mutex
	path string
}

// Open opens (or creates) the event log in dataDir, trimming if too large.
func Open(dataDir string) (*Log, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	l := &Log{path: filepath.Join(dataDir, logFile)}
	trimIfNeeded(l.path)
	return l, nil
}

// trimIfNeeded rewrites the log to its last trimToLines lines when it exceeds
// maxLogLines. Atomic write-then-rename; errors are silently ignored.
func trimIfNeeded(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	var lines [][]byte
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		b := sc.Bytes()
		cp := make([]byte, len(b))
		copy(cp, b)
		lines = append(lines, cp)
	}
	_ = f.Close()
	if len(lines) <= maxLogLines {
		return
	}
	lines = lines[len(lines)-trimToLines:]
	var buf bytes.Buffer
	for _, l := range lines {
		buf.Write(l)
		buf.WriteByte('\n')
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
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

// Recorder reports status through inner and records it in the log, tagging
// every event with the operation it belongs to. Status records an ordinary
// event; Final records the one that closes the operation.
type Recorder struct {
	log              *Log
	inner            iostream.StatusFunc
	tag              Event
	pendingCommandID string
}

// Recorder returns a Recorder that reports through inner and appends each call
// to the log, tagged with op, sidecarID, sidecarName, and branch.
func (l *Log) Recorder(inner iostream.StatusFunc, op Op, sidecarID, sidecarName, branch string) *Recorder {
	return &Recorder{
		log:   l,
		inner: inner,
		tag: Event{
			SidecarID:   sidecarID,
			SidecarName: sidecarName,
			Branch:      branch,
			Op:          op,
		},
	}
}

// Record returns a Recorder writing to the log in dataDir. Errors opening the
// log are silently ignored (best-effort; never blocks) and the Recorder then
// only reports through fn.
func Record(dataDir string, fn iostream.StatusFunc, op Op, sidecarID, sidecarName, branch string) *Recorder {
	el, err := Open(dataDir)
	if err != nil {
		return &Recorder{inner: fn, tag: Event{Op: op}}
	}
	return el.Recorder(fn, op, sidecarID, sidecarName, branch)
}

// SetCommandID records the sandbox-provisioner command ID of the exec that just
// finished. The next pass/fail event for that command will carry this ID.
// Pass "" when an exec failed without reporting an ID so a stale one from a
// prior exec is not attributed to it.
func (r *Recorder) SetCommandID(id string) {
	r.pendingCommandID = id
}

// Status reports an ordinary event, one that leaves the operation open.
func (r *Recorder) Status(level iostream.Level, msg string) {
	r.record(level, msg, false, 0, 0)
}

// Final reports the event that closes the operation, tallying the commands that
// passed out of those attempted. Both are zero when the operation failed before
// running anything.
func (r *Recorder) Final(level iostream.Level, msg string, passed, total int) {
	r.record(level, msg, true, passed, total)
}

func (r *Recorder) record(level iostream.Level, msg string, final bool, passed, total int) {
	if r.inner != nil {
		r.inner(level, msg)
	}
	if r.log == nil {
		return
	}
	e := r.tag
	e.Ts = time.Now()
	e.Level = levelStr(level)
	e.Msg = msg
	e.Final, e.Passed, e.Total = final, passed, total
	// Attach the pending command ID to the per-command pass/fail event. Final
	// events are run-wide summaries that belong to no single command.
	if !final && (level == iostream.LevelDone || level == iostream.LevelError) {
		e.CommandID = r.pendingCommandID
		r.pendingCommandID = ""
	}
	_ = r.log.Append(e)
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
		return levelStep
	case iostream.LevelInfo:
		return levelInfo
	case iostream.LevelWarn:
		return levelWarn
	case iostream.LevelDone:
		return levelDone
	case iostream.LevelError:
		return levelError
	default:
		return levelInfo
	}
}

package watchd

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
)

const (
	// MaxCommandBytes caps retained output per command. Output is capped in bytes
	// rather than lines because a verbose test suite emits megabytes in seconds
	// and a line count says nothing about memory. The tail is what survives: the
	// end of a failed run is the part anyone wants to read.
	MaxCommandBytes = 256 << 10

	// MaxCommands caps retained commands per project. Only finished commands are
	// evicted, so a project running more than this many at once keeps them all.
	MaxCommands = 20

	// authRetryInterval bounds how often the daemon re-resolves credentials after
	// a failure. Resolution can touch the OS keychain, so retrying it on every
	// registration would turn a missing token into a stream of keychain reads.
	authRetryInterval = 30 * time.Second
)

// CommandReg is the registration a process sends after submitting a remote
// command, so the daemon can stream and buffer that command's output. The
// submitting process may exit immediately afterwards — that is the whole point,
// since most remote commands are run by a hook that exits as soon as the command
// finishes.
type CommandReg struct {
	CommandID   string    `json:"command_id"`
	SidecarID   string    `json:"sidecar_id"`
	ProjectRoot string    `json:"project_root"`
	Op          string    `json:"op"`
	Name        string    `json:"name"`
	SubmittedAt time.Time `json:"submitted_at"`
}

// OutputChunk is one response to an output read.
type OutputChunk struct {
	// Data is raw command output, exactly as the remote command wrote it —
	// interleaved stdout and stderr, ANSI and carriage returns intact.
	Data []byte `json:"data"`
	// NextOffset is the offset to pass on the following read.
	NextOffset int64 `json:"next_offset"`
	Running    bool  `json:"running"`
	ExitCode   *int  `json:"exit_code,omitempty"`
	// Truncated reports that output before the returned data was evicted and is
	// gone. Saying so is the difference between showing a partial run and
	// showing a partial run that looks whole.
	Truncated bool `json:"truncated"`
	// Found is false when the daemon knows nothing about the command.
	Found bool `json:"found"`
	// Error explains why streaming stopped early, when it did. Without it a
	// failed stream is indistinguishable from a command that produced no output,
	// which sends the reader looking for a bug in their own command.
	Error string `json:"error,omitempty"`
}

// buffer holds one command's output, bounded in bytes, keeping the tail.
//
// Offsets exposed to readers are logical positions in the command's whole output
// stream, counting bytes that have already been evicted. That way a reader's
// offset stays meaningful across an eviction: it lands before the retained
// window, which is exactly the condition that means "you missed some".
type buffer struct {
	mu       sync.Mutex
	data     []byte
	dropped  int64 // bytes evicted from the head
	running  bool
	exitCode *int
	endedAt  time.Time
	// streamErr records why streaming stopped early, so an empty pane can say
	// why it is empty instead of looking like a command that printed nothing.
	streamErr string
}

func newBuffer() *buffer {
	return &buffer{running: true}
}

func (b *buffer) append(p []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = append(b.data, p...)
	if excess := len(b.data) - MaxCommandBytes; excess > 0 {
		// Copy the tail down rather than reslicing, so the underlying array is
		// reused instead of growing without bound behind a moving window.
		b.data = append(b.data[:0], b.data[excess:]...)
		b.dropped += int64(excess)
	}
}

// finish marks the command terminated. exitCode may be nil when the stream
// failed without reporting one; streamErr says why in that case, and is empty on
// a clean exit.
func (b *buffer) finish(exitCode *int, streamErr string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.running = false
	b.exitCode = exitCode
	b.endedAt = time.Now()
	b.streamErr = streamErr
}

// read returns output at or after offset. An offset before the retained window
// returns the whole window with Truncated set; an offset past the end returns no
// data, which is the normal "nothing new" case while tailing.
func (b *buffer) read(offset int64) OutputChunk {
	b.mu.Lock()
	defer b.mu.Unlock()

	total := b.dropped + int64(len(b.data))
	chunk := OutputChunk{
		Running:    b.running,
		ExitCode:   b.exitCode,
		NextOffset: total,
		Found:      true,
		Error:      b.streamErr,
	}
	switch {
	case offset < b.dropped:
		chunk.Truncated = true
		chunk.Data = append([]byte(nil), b.data...)
	case offset >= total:
		// Caller is current. Leave Data nil.
	default:
		chunk.Data = append([]byte(nil), b.data[offset-b.dropped:]...)
	}
	return chunk
}

func (b *buffer) state() (running bool, exitCode *int, endedAt time.Time, size int64, truncated bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.running, b.exitCode, b.endedAt, b.dropped + int64(len(b.data)), b.dropped > 0
}

// commandEntry is one registered command and its in-flight streamer.
type commandEntry struct {
	reg    CommandReg
	buf    *buffer
	cancel context.CancelFunc
}

// outputStore owns every registered command's buffer and streamer.
//
// It is deliberately in-memory: a daemon restart loses it. Recovering across a
// restart needs a durable record of command IDs, which is what the event log's
// command_id field is for once that lands.
type outputStore struct {
	// parent bounds every streamer this store starts. Deriving from it rather
	// than context.Background() ties a streamer's lifetime to the daemon's own
	// structurally, instead of leaving it to stopAll being deferred correctly —
	// a guarantee that is invisible from register, where the goroutine starts.
	parent context.Context

	mu        sync.Mutex
	cmds      map[string]*commandEntry
	byProject map[string][]string // project root → command IDs, oldest first
}

func newOutputStore(parent context.Context) *outputStore {
	if parent == nil {
		parent = context.Background()
	}
	return &outputStore{
		parent:    parent,
		cmds:      make(map[string]*commandEntry),
		byProject: make(map[string][]string),
	}
}

// streamFn consumes a command's output, calling onOutput for each run of bytes.
// It exists so tests can drive the store without an API client.
type streamFn func(ctx context.Context, commandID string, onOutput func([]byte)) (*circleci.ExecResponse, error)

// register records a command and starts streaming it. A command ID already known
// is ignored, so a duplicate registration cannot start a second streamer for the
// same output.
func (s *outputStore) register(reg CommandReg, stream streamFn) {
	if reg.CommandID == "" {
		return
	}
	s.mu.Lock()
	if _, exists := s.cmds[reg.CommandID]; exists {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(s.parent)
	entry := &commandEntry{reg: reg, buf: newBuffer(), cancel: cancel}
	s.cmds[reg.CommandID] = entry
	s.byProject[reg.ProjectRoot] = append(s.byProject[reg.ProjectRoot], reg.CommandID)
	s.evictLocked(reg.ProjectRoot)
	s.mu.Unlock()

	if stream == nil {
		// No client available. The command is recorded so the dashboard can say
		// it exists, but there is no output to serve for it.
		entry.buf.finish(nil, "no CircleCI credentials, so output was never streamed")
		cancel()
		return
	}

	go func() {
		defer cancel()
		resp, err := stream(ctx, reg.CommandID, entry.buf.append)
		if err != nil {
			// A cancelled stream is a shutdown, not a failure worth logging.
			if ctx.Err() == nil {
				log.Printf("watchd: stream output for %s: %v", reg.CommandID, err)
			}
			entry.buf.finish(nil, err.Error())
			return
		}
		code := resp.ExitCode
		entry.buf.finish(&code, "")
	}()
}

// evictLocked drops finished commands until the project is under MaxCommands.
// A running command is never evicted: its streamer is still writing, and
// dropping it would lose output that is still arriving.
func (s *outputStore) evictLocked(root string) {
	ids := s.byProject[root]
	// Written back on every exit path: the loop can drop entries and then bail
	// out with everything remaining still running, and skipping the write-back
	// there would leave the index holding an evicted ID and a duplicated tail.
	defer func() { s.byProject[root] = ids }()
	for len(ids) > MaxCommands {
		victim := -1
		for i, id := range ids {
			entry, ok := s.cmds[id]
			if !ok {
				victim = i
				break
			}
			if running, _, _, _, _ := entry.buf.state(); !running {
				victim = i
				break
			}
		}
		if victim < 0 {
			return // everything still running
		}
		if entry, ok := s.cmds[ids[victim]]; ok {
			entry.cancel()
			delete(s.cmds, ids[victim])
		}
		ids = append(ids[:victim], ids[victim+1:]...)
	}
}

// read returns output for a command. An unknown command yields Found false
// rather than an error: the dashboard asking about a command the daemon has
// forgotten is ordinary, not exceptional.
func (s *outputStore) read(commandID string, offset int64) OutputChunk {
	s.mu.Lock()
	entry, ok := s.cmds[commandID]
	s.mu.Unlock()
	if !ok {
		return OutputChunk{}
	}
	return entry.buf.read(offset)
}

// commandsFor returns the command states for one project, oldest first.
func (s *outputStore) commandsFor(root string) []CommandState {
	s.mu.Lock()
	ids := append([]string(nil), s.byProject[root]...)
	entries := make([]*commandEntry, 0, len(ids))
	for _, id := range ids {
		if entry, ok := s.cmds[id]; ok {
			entries = append(entries, entry)
		}
	}
	s.mu.Unlock()

	out := make([]CommandState, 0, len(entries))
	for _, entry := range entries {
		running, exitCode, endedAt, size, truncated := entry.buf.state()
		cs := CommandState{
			CommandID:   entry.reg.CommandID,
			SidecarID:   entry.reg.SidecarID,
			Op:          entry.reg.Op,
			Name:        entry.reg.Name,
			SubmittedAt: entry.reg.SubmittedAt,
			ExitCode:    exitCode,
			Running:     running,
			Bytes:       size,
			Truncated:   truncated,
		}
		if !endedAt.IsZero() {
			ended := endedAt
			cs.EndedAt = &ended
		}
		out = append(out, cs)
	}
	return out
}

// stopAll cancels every streamer. Called on daemon shutdown.
func (s *outputStore) stopAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, entry := range s.cmds {
		entry.cancel()
	}
}

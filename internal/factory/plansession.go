package factory

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gofrs/flock"
)

// PlanSession is this machine's record of a Claude session, so a chat opened
// later resumes that session's conversation rather than starting a blank one.
// One chunk process at a time holds it open: two resuming the same session
// would both write its conversation.
type PlanSession struct {
	path string
	lock *flock.Flock
}

// ErrChatOpen is returned by OpenChatSession while another
// process, such as a second chunk daemon, has the record open.
var ErrChatOpen = errors.New("the repository's chat is open in another chunk daemon")

// chatSessionPath is where the repository at root's chat
// session is recorded, beside its factory worktrees.
func chatSessionPath(root string) (string, error) {
	dir, err := runsDir(root)
	if err != nil {
		return "", fmt.Errorf("find the chat's record: %w", err)
	}
	return filepath.Join(dir, "chat.session"), nil
}

// OpenChatSession opens the record of the chat session for the
// repository at root, failing with ErrChatOpen while another process
// has it open. The caller must Close it.
func OpenChatSession(root string) (*PlanSession, error) {
	path, err := chatSessionPath(root)
	if err != nil {
		return nil, err
	}
	return openSessionRecord(path, ErrChatOpen)
}

// openSessionRecord opens the session record at path, making its directory
// and taking its lock, failing with busy while another process holds it.
func openSessionRecord(path string, busy error) (*PlanSession, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("make the session record's directory: %w", err)
	}
	lock := flock.New(path + ".lock")
	ok, err := lock.TryLock()
	if err != nil {
		return nil, fmt.Errorf("lock the session record: %w", err)
	}
	if !ok {
		return nil, busy
	}
	return &PlanSession{path: path, lock: lock}, nil
}

// ID is the session last recorded, "" when none has been.
func (s *PlanSession) ID() (string, error) { return readSessionFile(s.path) }

// readSessionFile is the session recorded at path, "" when none is.
func readSessionFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read the session record: %w", err)
	}
	return strings.TrimSpace(string(b)), nil
}

// Record records id as the session the record is for.
func (s *PlanSession) Record(id string) error {
	if err := os.WriteFile(s.path, []byte(id+"\n"), 0o600); err != nil {
		return fmt.Errorf("write the session record: %w", err)
	}
	return nil
}

// Close lets another process open the session record.
func (s *PlanSession) Close() error {
	return s.lock.Unlock()
}

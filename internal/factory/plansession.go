package factory

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gofrs/flock"
)

// ErrPlanSessionOpen is returned by OpenPlanSession for a plan another chunk
// process on this machine is planning.
var ErrPlanSessionOpen = errors.New("the plan is open for planning in another chunk process")

// PlanSession is this machine's record of the Claude session planning a
// factory plan, so a planning screen opened on the plan later resumes that
// session's conversation rather than starting a blank one. One chunk process
// at a time holds it open: two resuming the same session would both write
// its conversation.
type PlanSession struct {
	path string
	lock *flock.Flock
}

// planSessionPath is where the session planning plan planID is recorded,
// beside the plan's worktree rather than in it, so the record is not a file
// in the plan's change.
func planSessionPath(root, planID string) (string, error) {
	path, err := PlanWorktreePath(root, planID)
	if err != nil {
		return "", err
	}
	return path + ".session", nil
}

// OpenPlanSession opens the record of the session planning plan planID in
// the repository at root, failing with ErrPlanSessionOpen while another
// process has it open. The caller must Close it.
func OpenPlanSession(root, planID string) (*PlanSession, error) {
	path, err := planSessionPath(root, planID)
	if err != nil {
		return nil, err
	}
	return openSessionRecord(path, ErrPlanSessionOpen)
}

// ErrChatOpen is returned by OpenChatSession while another
// process, such as a second chunk daemon, has the record open.
var ErrChatOpen = errors.New("the repository's chat is open in another chunk daemon")

// chatSessionPath is where the repository at root's chat
// session is recorded, beside its plans' worktrees.
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

// RecordedPlanSession is the session last recorded as planning plan planID
// in the repository at root, "" when none is, read without opening the
// record, so it can be read while another process holds it open.
func RecordedPlanSession(root, planID string) (string, error) {
	path, err := planSessionPath(root, planID)
	if err != nil {
		return "", err
	}
	return readSessionFile(path)
}

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

// dropPlanSession deletes the record of the session that planned plan
// planID, once the plan is over.
func dropPlanSession(root, planID string) {
	path, err := planSessionPath(root, planID)
	if err != nil {
		return
	}
	_ = os.Remove(path)
	_ = os.Remove(path + ".lock")
}

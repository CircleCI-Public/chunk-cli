package factory

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
)

// claudeBinary is the Claude Code command factory sessions run.
const claudeBinary = "claude"

// ErrNoClaude is returned when Claude Code is not installed on the
// developer's machine, so there is nothing to run a factory session with.
var ErrNoClaude = errors.New("claude is not installed")

// CheckClaude returns ErrNoClaude when there is no claude on PATH, so a
// caller can refuse before it starts a session.
func CheckClaude() error {
	if _, err := exec.LookPath(claudeBinary); err != nil {
		return ErrNoClaude
	}
	return nil
}

const flagDisallowedTools = "--disallowedTools"

// outputTail bounds how much of claude's stderr is kept.
const outputTail = 4000

// limitWriter keeps the start of stderr, up to outputTail, and drops the rest.
type limitWriter struct{ buf *bytes.Buffer }

func (w *limitWriter) Write(p []byte) (int, error) {
	if w.buf.Len() < outputTail {
		w.buf.Write(p)
	}
	return len(p), nil
}

// runsDir is where a repository's factory worktrees are kept. They belong to
// the repository, so every checkout of it sees them.
func runsDir(root string) (string, error) {
	dataDir, err := config.RepoDataDir(root)
	if err != nil {
		return "", fmt.Errorf("find chunk's data directory for the repository: %w", err)
	}
	return dataDir, nil
}

package validate

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
)

// ErrNotConfigured indicates no validate commands are configured.
var ErrNotConfigured = errors.New("no validate commands configured")

// ErrWorkspaceNotFound is returned when the remote workspace directory does not exist.
var ErrWorkspaceNotFound = errors.New("workspace directory not found on sidecar")

// DefaultTimeout is the per-command execution timeout in seconds.
const DefaultTimeout = 300

// List prints all configured command names and their run strings.
func List(cfg *config.ProjectConfig, status iostream.StatusFunc) error {
	if !cfg.HasCommands() {
		status(iostream.LevelInfo, "No commands configured.")
		status(iostream.LevelInfo, "Add commands with: chunk validate <name> --cmd \"your command\" --save")
		return nil
	}
	for _, c := range cfg.Commands {
		status(iostream.LevelInfo, fmt.Sprintf("%s: %s", c.Name, c.Run))
	}
	return nil
}

// --- Hook lifecycle ---

// HookExitError signals a specific process exit code without printing
// additional error output. All output must be written before this error
// is returned.
type HookExitError struct {
	code int
}

func (e *HookExitError) Error() string { return fmt.Sprintf("exit %d", e.code) }
func (e *HookExitError) ExitCode() int { return e.code }

// NewHookExitError returns a HookExitError with the given exit code.
func NewHookExitError(code int) error { return &HookExitError{code: code} }

// HooksDisabled reports whether chunk validate hooks are currently suppressed.
// envDisabled should be set by the caller from CHUNK_HOOKS_DISABLED; it returns
// true when that flag is set or the sentinel file .chunk/hooks-disabled exists
// under workDir. On any error other than ErrNotExist the function fails open
// (returns false) so hooks continue to run when the check is uncertain.
func HooksDisabled(workDir string, envDisabled bool) bool {
	if envDisabled {
		return true
	}
	_, err := os.Stat(filepath.Join(workDir, ".chunk", "hooks-disabled"))
	return err == nil
}

// HasGitChanges reports whether the working tree at workDir has any
// uncommitted modifications (staged or unstaged). Returns true when git
// is unavailable or the directory is not a repository so that validation
// still runs in ambiguous cases.
func HasGitChanges(workDir string) bool {
	out, err := exec.Command("git", "-C", workDir, "status", "--porcelain").Output()
	if err != nil {
		return true // fail open: run validation when git is unavailable
	}
	return strings.TrimSpace(string(out)) != ""
}

// WrapHookResult applies Stop hook lifecycle to the result of running validate
// commands. On success it resets the attempt counter. On failure it increments
// the counter and returns a HookExitError with code 2 to re-signal the agent,
// or prints a give-up message and returns nil once maxAttempts is reached.
func WrapHookResult(sessionID string, execErr error, maxAttempts int, warn io.Writer) error {
	if execErr == nil {
		ResetAttempts(sessionID)
		return nil
	}
	n := TrackFailedAttempt(sessionID, warn)
	if n >= maxAttempts {
		_, _ = fmt.Fprintf(warn, "chunk validate: validation has failed %d time(s) in a row.\n", n)
		_, _ = fmt.Fprintf(warn, "The failures above do not appear to be resolving automatically.\n")
		_, _ = fmt.Fprintf(warn, "Stop attempting to fix this and ask the user for guidance instead.\n")
		return nil
	}
	return &HookExitError{code: 2}
}

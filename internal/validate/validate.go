package validate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
)

// ErrNotConfigured indicates no validate commands are configured.
var ErrNotConfigured = errors.New("no validate commands configured")

// RunStatus represents the outcome of a single command execution.
type RunStatus int

// RunPassed, RunFailed, RunSkipped are the possible outcomes of a command run.
const (
	RunPassed RunStatus = iota
	RunFailed
	RunSkipped
)

// CommandResult records the outcome of one command execution.
type CommandResult struct {
	Name     string
	Duration time.Duration
	ExitCode int
	Status   RunStatus
	Timeout  bool
}

// ErrWorkspaceNotFound is returned when the remote workspace directory does not exist.
var ErrWorkspaceNotFound = errors.New("workspace directory not found on sidecar")

// WorkspaceExists checks whether dest exists as a directory on the remote sidecar.
func WorkspaceExists(ctx context.Context, execFn func(context.Context, string) (string, string, int, error), dest string) error {
	_, _, exitCode, err := execFn(ctx, "test -d "+shellEscape(dest))
	if err != nil {
		return err
	}
	if exitCode != 0 {
		return ErrWorkspaceNotFound
	}
	return nil
}

// shellEscape wraps arg in single quotes for safe use in a POSIX sh -c command.
func shellEscape(arg string) string {
	return "'" + strings.ReplaceAll(arg, "'", "'\\''") + "'"
}

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

// RunInline runs an inline command string.
func RunInline(ctx context.Context, workDir, name, command string, status iostream.StatusFunc, streams iostream.Streams) error {
	return runCommand(ctx, workDir, name, command, 0, status, streams)
}

// RunNamed runs a single named command from config.
func RunNamed(ctx context.Context, workDir, name string, cfg *config.ProjectConfig, status iostream.StatusFunc, streams iostream.Streams) error {
	c := cfg.FindCommand(name)
	if c == nil {
		return fmt.Errorf("command %q not configured", name)
	}
	return runCommand(ctx, workDir, c.Name, c.Run, c.Timeout, status, streams)
}

// RunAllWithResults runs all configured commands and returns per-command results
// alongside the first error encountered.
func RunAllWithResults(ctx context.Context, workDir string, cfg *config.ProjectConfig, status iostream.StatusFunc, streams iostream.Streams) ([]CommandResult, error) {
	if !cfg.HasCommands() {
		return nil, ErrNotConfigured
	}
	results := make([]CommandResult, 0, len(cfg.Commands))
	for i, c := range cfg.Commands {
		start := time.Now()
		err := runCommand(ctx, workDir, c.Name, c.Run, c.Timeout, status, streams)
		dur := time.Since(start)
		if err != nil {
			var timeoutErr *commandTimeoutError
			isTimeout := errors.As(err, &timeoutErr)
			exitCode := 0
			if !isTimeout {
				exitCode = exitCodeFromErr(err)
			}
			results = append(results, CommandResult{Name: c.Name, Duration: dur, ExitCode: exitCode, Status: RunFailed, Timeout: isTimeout})
			for j := i + 1; j < len(cfg.Commands); j++ {
				status(iostream.LevelWarn, fmt.Sprintf("%s: skipped (%s failed)", cfg.Commands[j].Name, c.Name))
				results = append(results, CommandResult{Name: cfg.Commands[j].Name, Status: RunSkipped})
			}
			return results, err
		}
		results = append(results, CommandResult{Name: c.Name, Duration: dur, Status: RunPassed})
	}
	return results, nil
}

// RunAll runs all configured commands, stopping at the first failure.
func RunAll(ctx context.Context, workDir string, cfg *config.ProjectConfig, status iostream.StatusFunc, streams iostream.Streams) error {
	_, err := RunAllWithResults(ctx, workDir, cfg, status, streams)
	return err
}

// PrintSummary writes a per-command result table to w.
func PrintSummary(results []CommandResult, w io.Writer) {
	if len(results) == 0 {
		return
	}
	anyFailed := false
	for _, r := range results {
		if r.Status == RunFailed {
			anyFailed = true
			break
		}
	}
	if anyFailed {
		_, _ = fmt.Fprintln(w, "\nValidation failed:")
	} else {
		_, _ = fmt.Fprintln(w, "\nValidation passed:")
	}
	maxName := 0
	for _, r := range results {
		if len(r.Name) > maxName {
			maxName = len(r.Name)
		}
	}
	for _, r := range results {
		var symbol, detail string
		switch r.Status {
		case RunPassed:
			symbol = "✓"
			detail = fmt.Sprintf("%.1fs", r.Duration.Seconds())
		case RunFailed:
			symbol = "✗"
			if r.Timeout {
				detail = fmt.Sprintf("%.1fs   timeout", r.Duration.Seconds())
			} else {
				detail = fmt.Sprintf("%.1fs   exit %d", r.Duration.Seconds(), r.ExitCode)
			}
		case RunSkipped:
			symbol = "—"
			detail = "skipped"
		}
		_, _ = fmt.Fprintf(w, "  %-*s   %s   %s\n", maxName, r.Name, symbol, detail)
	}
}

// LastFailed returns the first CommandResult with RunFailed status, or nil.
func LastFailed(results []CommandResult) *CommandResult {
	for i := range results {
		if results[i].Status == RunFailed {
			return &results[i]
		}
	}
	return nil
}

// RunDryRun prints commands without executing them.
func RunDryRun(cfg *config.ProjectConfig, name string, status iostream.StatusFunc) error {
	if !cfg.HasCommands() {
		return ErrNotConfigured
	}

	commands := cfg.Commands
	if name != "" {
		c := cfg.FindCommand(name)
		if c == nil {
			return fmt.Errorf("command %q not configured", name)
		}
		commands = []config.Command{*c}
	}

	for _, c := range commands {
		status(iostream.LevelInfo, fmt.Sprintf("%s: %s", c.Name, c.Run))
	}
	return nil
}

// RunRemoteWithResults runs commands on a remote sidecar via SSH and returns
// per-command results alongside the first error encountered.
// If name is non-empty, only the named command is run.
// workDir is the local repository root used to expand {{CHANGED_PACKAGES}}.
func RunRemoteWithResults(ctx context.Context, execFn func(ctx context.Context, script string) (stdout, stderr string, exitCode int, err error), cfg *config.ProjectConfig, name, dest, workDir string, status iostream.StatusFunc, streams iostream.Streams) ([]CommandResult, error) {
	commands := cfg.Commands
	if name != "" {
		c := cfg.FindCommand(name)
		if c == nil {
			return nil, fmt.Errorf("command %q not configured", name)
		}
		commands = []config.Command{*c}
	}
	results := make([]CommandResult, 0, len(commands))
	for i, c := range commands {
		run := expandCommand(workDir, c.Run)
		script := "cd " + shellEscape(dest) + " && " + run
		status(iostream.LevelInfo, fmt.Sprintf("Running %s (remote): %s", c.Name, c.Run))
		start := time.Now()
		stdout, stderr, exitCode, err := execFn(ctx, script)
		dur := time.Since(start)
		if stdout != "" {
			_, _ = fmt.Fprint(streams.Out, stdout)
		}
		if stderr != "" {
			_, _ = fmt.Fprint(streams.Err, stderr)
		}
		if err != nil {
			results = append(results, CommandResult{Name: c.Name, Duration: dur, ExitCode: exitCode, Status: RunFailed})
			for j := i + 1; j < len(commands); j++ {
				results = append(results, CommandResult{Name: commands[j].Name, Status: RunSkipped})
			}
			return results, fmt.Errorf("remote %s: %w", c.Name, err)
		}
		if exitCode != 0 {
			results = append(results, CommandResult{Name: c.Name, Duration: dur, ExitCode: exitCode, Status: RunFailed})
			for j := i + 1; j < len(commands); j++ {
				results = append(results, CommandResult{Name: commands[j].Name, Status: RunSkipped})
			}
			return results, fmt.Errorf("remote %s failed with exit code %d", c.Name, exitCode)
		}
		results = append(results, CommandResult{Name: c.Name, Duration: dur, Status: RunPassed})
	}
	return results, nil
}

// RunRemote runs commands on a remote sidecar via SSH.
// If name is non-empty, only the named command is run.
// workDir is the local repository root used to expand {{CHANGED_PACKAGES}}.
func RunRemote(ctx context.Context, execFn func(ctx context.Context, script string) (stdout, stderr string, exitCode int, err error), cfg *config.ProjectConfig, name, dest, workDir string, status iostream.StatusFunc, streams iostream.Streams) error {
	_, err := RunRemoteWithResults(ctx, execFn, cfg, name, dest, workDir, status, streams)
	return err
}

// RunRemoteInline runs a single inline command on a remote sidecar via SSH.
func RunRemoteInline(ctx context.Context, execFn func(ctx context.Context, script string) (stdout, stderr string, exitCode int, err error), name, command, dest string, status iostream.StatusFunc, streams iostream.Streams) error {
	script := "cd " + shellEscape(dest) + " && " + command
	status(iostream.LevelInfo, fmt.Sprintf("Running %s (remote): %s", name, command))
	stdout, stderr, exitCode, err := execFn(ctx, script)
	if err != nil {
		return fmt.Errorf("remote %s: %w", name, err)
	}
	if stdout != "" {
		_, _ = fmt.Fprint(streams.Out, stdout)
	}
	if stderr != "" {
		_, _ = fmt.Fprint(streams.Err, stderr)
	}
	if exitCode != 0 {
		return fmt.Errorf("remote %s failed with exit code %d", name, exitCode)
	}
	return nil
}

// expandCommand replaces template variables in command before execution.
// {{CHANGED_PACKAGES}} expands to the space-separated list of Go package
// paths whose source files appear in `git diff HEAD`.
// Expands to "./..." when no .go files changed.
func expandCommand(workDir, command string) string {
	if !strings.Contains(command, "{{CHANGED_PACKAGES}}") {
		return command
	}

	out, err := exec.Command("git", "-C", workDir, "diff", "HEAD", "--name-only").Output()
	if err != nil {
		return strings.ReplaceAll(command, "{{CHANGED_PACKAGES}}", "./...")
	}

	seen := map[string]bool{}
	var pkgs []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" || !strings.HasSuffix(line, ".go") {
			continue
		}
		pkg := "./" + filepath.Dir(line)
		if !seen[pkg] {
			seen[pkg] = true
			pkgs = append(pkgs, pkg)
		}
	}

	expanded := "./..."
	if len(pkgs) > 0 {
		expanded = strings.Join(pkgs, " ")
	}
	return strings.ReplaceAll(command, "{{CHANGED_PACKAGES}}", expanded)
}

type commandTimeoutError struct {
	name    string
	timeout int
}

func (e *commandTimeoutError) Error() string {
	return fmt.Sprintf("%s command timed out after %ds", e.name, e.timeout)
}

func exitCodeFromErr(err error) int {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return 1
}

func runCommand(ctx context.Context, workDir, name, command string, timeoutSec int, status iostream.StatusFunc, streams iostream.Streams) error {
	command = expandCommand(workDir, command)
	status(iostream.LevelInfo, fmt.Sprintf("Running %s: %s", name, command))

	if timeoutSec <= 0 {
		timeoutSec = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = workDir
	cmd.Stdout = streams.Out
	cmd.Stderr = streams.Err

	err := cmd.Run()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return &commandTimeoutError{name: name, timeout: timeoutSec}
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() != 0 {
			return fmt.Errorf("%s command failed with exit code %d: %w", name, exitErr.ExitCode(), err)
		}
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

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
// lastFailure is optional: when non-nil its name and exit info are included in
// the give-up message so the agent has structured context about what failed.
func WrapHookResult(sessionID string, execErr error, maxAttempts int, lastFailure *CommandResult, warn io.Writer) error {
	if execErr == nil {
		ResetAttempts(sessionID)
		return nil
	}
	n := TrackFailedAttempt(sessionID, warn)
	if n >= maxAttempts {
		_, _ = fmt.Fprintf(warn, "chunk validate: validation has failed %d time(s) in a row.\n", n)
		if lastFailure != nil {
			if lastFailure.Timeout {
				_, _ = fmt.Fprintf(warn, "Last failure: %s (timed out after %.0fs)\n", lastFailure.Name, lastFailure.Duration.Seconds())
			} else {
				_, _ = fmt.Fprintf(warn, "Last failure: %s (exit %d, %.1fs)\n", lastFailure.Name, lastFailure.ExitCode, lastFailure.Duration.Seconds())
			}
		}
		_, _ = fmt.Fprintf(warn, "The failures above do not appear to be resolving automatically.\n")
		_, _ = fmt.Fprintf(warn, "Stop attempting to fix this and ask the user for guidance instead.\n")
		return nil
	}
	return &HookExitError{code: 2}
}

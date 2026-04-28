package validate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
)

// ErrNotConfigured indicates no validate commands are configured.
var ErrNotConfigured = errors.New("no validate commands configured")

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
func RunInline(ctx context.Context, workDir, name, command string, status iostream.StatusFunc, streams iostream.Streams) ([]CommandResult, error) {
	err := runCommand(ctx, workDir, name, command, 0, status, streams)
	return []CommandResult{{Name: name, Passed: err == nil}}, err
}

// RunNamed runs a single named command from config.
func RunNamed(ctx context.Context, workDir, name string, cfg *config.ProjectConfig, status iostream.StatusFunc, streams iostream.Streams) ([]CommandResult, error) {
	c := cfg.FindCommand(name)
	if c == nil {
		return nil, fmt.Errorf("command %q not configured", name)
	}
	err := runCommand(ctx, workDir, c.Name, c.Run, c.Timeout, status, streams)
	return []CommandResult{{Name: c.Name, Passed: err == nil}}, err
}

// RunAll runs all configured commands, stopping at the first failure.
func RunAll(ctx context.Context, workDir string, cfg *config.ProjectConfig, status iostream.StatusFunc, streams iostream.Streams) ([]CommandResult, error) {
	if !cfg.HasCommands() {
		return nil, ErrNotConfigured
	}

	var results []CommandResult
	for i, c := range cfg.Commands {
		err := runCommand(ctx, workDir, c.Name, c.Run, c.Timeout, status, streams)
		results = append(results, CommandResult{Name: c.Name, Passed: err == nil})
		if err != nil {
			for j := i + 1; j < len(cfg.Commands); j++ {
				status(iostream.LevelWarn, fmt.Sprintf("%s: skipped (%s failed)", cfg.Commands[j].Name, c.Name))
			}
			return results, err
		}
	}
	return results, nil
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

// RunRemote runs commands on a remote sidecar via SSH.
// If name is non-empty, only the named command is run.
func RunRemote(ctx context.Context, execFn func(ctx context.Context, script string) (stdout, stderr string, exitCode int, err error), cfg *config.ProjectConfig, name, dest string, status iostream.StatusFunc, streams iostream.Streams) error {
	commands := cfg.Commands
	if name != "" {
		c := cfg.FindCommand(name)
		if c == nil {
			return fmt.Errorf("command %q not configured", name)
		}
		commands = []config.Command{*c}
	}
	for _, c := range commands {
		script := "cd " + shellEscape(dest) + " && " + c.Run
		status(iostream.LevelInfo, fmt.Sprintf("Running %s (remote): %s", c.Name, c.Run))
		stdout, stderr, exitCode, err := execFn(ctx, script)
		if err != nil {
			return fmt.Errorf("remote %s: %w", c.Name, err)
		}
		if stdout != "" {
			_, _ = fmt.Fprint(streams.Out, stdout)
		}
		if stderr != "" {
			_, _ = fmt.Fprint(streams.Err, stderr)
		}
		if exitCode != 0 {
			return fmt.Errorf("remote %s failed with exit code %d", c.Name, exitCode)
		}
	}
	return nil
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
			return fmt.Errorf("%s command timed out after %ds", name, timeoutSec)
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() != 0 {
			return fmt.Errorf("%s command failed with exit code %d", name, exitErr.ExitCode())
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

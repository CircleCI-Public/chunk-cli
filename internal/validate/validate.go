package validate

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/commandutil"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/envctx"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/telemetry"
)

func formatElapsed(d time.Duration) string {
	return commandutil.FormatElapsed(d)
}

// ErrNotConfigured indicates no validate commands are configured.
var ErrNotConfigured = errors.New("no validate commands configured")

// Result holds the pass/fail counts from a validate run.
type Result struct {
	Passed int
	Total  int
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

// The variables a command with parallelism is told its place among its runs by.
const (
	EnvParallelTotal = "CIRCLE_PARALLEL_TOTAL"
	EnvParallelIndex = "CIRCLE_PARALLEL_INDEX"
)

// DefaultTimeout is the per-command execution timeout in seconds.
const DefaultTimeout = 300

// List prints all configured command names and their run strings, tagged with
// where each one runs and any role that decides it. Without the tags there is no
// way to see which commands `chunk validate --mark-remote` would change.
func List(cfg *config.ProjectConfig, status iostream.StatusFunc) error {
	if !cfg.HasCommands() {
		status(iostream.LevelInfo, "No commands configured.")
		status(iostream.LevelInfo, "Add commands with: chunk validate <name> --cmd \"your command\" --save")
		return nil
	}
	for _, c := range cfg.Commands {
		status(iostream.LevelInfo, fmt.Sprintf("%s [%s]: %s", c.Name, commandTags(c), c.Run))
	}
	return nil
}

// commandTags describes where a command runs, and its role and parallelism when
// it has them.
func commandTags(c config.Command) string {
	where := "remote"
	if c.RunsLocally() {
		where = "local"
	}
	if c.Role != "" {
		where += ", " + c.Role
	}
	if c.Parallelism >= 2 {
		where += fmt.Sprintf(", parallelism: %d", c.Parallelism)
	}
	return where
}

// RunInline runs an inline command string.
func RunInline(ctx context.Context, workDir, name, command string, envVars map[string]string, status iostream.StatusFunc, streams iostream.Streams) (Result, error) {
	if err := runCommand(ctx, workDir, name, command, 0, 0, envVars, status, streams); err != nil {
		return Result{Total: 1}, err
	}
	return Result{Passed: 1, Total: 1}, nil
}

// RunNamed runs a single named command from config.
func RunNamed(ctx context.Context, workDir, name string, cfg *config.ProjectConfig, envVars map[string]string, status iostream.StatusFunc, streams iostream.Streams) (Result, error) {
	c := cfg.FindCommand(name)
	if c == nil {
		return Result{}, fmt.Errorf("command %q not configured", name)
	}
	if err := runCommand(ctx, workDir, c.Name, c.Run, c.Timeout, 0, envVars, status, streams); err != nil {
		return Result{Total: 1}, err
	}
	return Result{Passed: 1, Total: 1}, nil
}

func skipRemaining(status iostream.StatusFunc, remaining []config.Command, width int) {
	commandutil.SkipRemaining(status, remaining, width)
}

func nameWidth(commands []config.Command) int {
	return commandutil.NameWidth(commands)
}

// RunAll runs all configured commands, stopping at the first failure. A command
// with Parallelism of 2 or more runs that many times in turn, each run with
// CIRCLE_PARALLEL_INDEX and CIRCLE_PARALLEL_TOTAL set, and counts once in the
// result; the first failed run stops the command and the commands after it.
func RunAll(ctx context.Context, workDir string, cfg *config.ProjectConfig, envVars map[string]string, status iostream.StatusFunc, streams iostream.Streams) (Result, error) {
	if !cfg.HasCommands() {
		return Result{}, ErrNotConfigured
	}

	maxWidth := nameWidth(cfg.Commands)
	total := len(cfg.Commands)
	for i, c := range cfg.Commands {
		if err := runCommandRuns(ctx, workDir, c, maxWidth, envVars, status, streams); err != nil {
			skipRemaining(status, cfg.Commands[i+1:], maxWidth)
			return Result{Passed: i, Total: total}, err
		}
	}
	return Result{Passed: total, Total: total}, nil
}

// runCommandRuns runs c once, or c.Parallelism times in turn when that is 2 or
// more, stopping at the first failed run.
func runCommandRuns(ctx context.Context, workDir string, c config.Command, nameWidth int, envVars map[string]string, status iostream.StatusFunc, streams iostream.Streams) error {
	if c.Parallelism < 2 {
		return runCommand(ctx, workDir, c.Name, c.Run, c.Timeout, nameWidth, envVars, status, streams)
	}
	for i := range c.Parallelism {
		// chunk's values are set last, so they override the caller's and the
		// command's own.
		env := make(map[string]string, len(envVars)+2)
		maps.Copy(env, envVars)
		env[EnvParallelTotal] = strconv.Itoa(c.Parallelism)
		env[EnvParallelIndex] = strconv.Itoa(i)
		label := fmt.Sprintf("%s [%d/%d]", c.Name, i+1, c.Parallelism)
		if err := runCommand(ctx, workDir, label, c.Run, c.Timeout, max(nameWidth, len(label)), env, status, streams); err != nil {
			return err
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
		if c.Parallelism >= 2 {
			status(iostream.LevelInfo, fmt.Sprintf("%s: %s (parallelism: %d)", c.Name, c.Run, c.Parallelism))
			continue
		}
		status(iostream.LevelInfo, fmt.Sprintf("%s: %s", c.Name, c.Run))
	}
	return nil
}

// RunRemote runs commands on a remote sidecar via SSH.
// If name is non-empty, only the named command is run.
// localWorkDir is used to expand {{CHANGED_PACKAGES}} against the local git diff
// before sending the command to the remote (which has a clean checkout).
func RunRemote(ctx context.Context, execFn func(ctx context.Context, script string) (stdout, stderr string, exitCode int, err error), cfg *config.ProjectConfig, name, dest, localWorkDir string, status iostream.StatusFunc, streams iostream.Streams) error {
	_, err := RunRemoteResult(ctx, execFn, cfg, name, dest, localWorkDir, status, streams)
	return err
}

// RunRemoteResult runs remote commands and reports how many completed before
// the first failure.
func RunRemoteResult(ctx context.Context, execFn func(ctx context.Context, script string) (stdout, stderr string, exitCode int, err error), cfg *config.ProjectConfig, name, dest, localWorkDir string, status iostream.StatusFunc, streams iostream.Streams) (Result, error) {
	return runRemote(ctx, execFn, cfg, name, dest, localWorkDir, status, streams, false)
}

// RunRemoteStreamed runs remote commands whose executor has already written
// stdout and stderr to streams as they arrive.
func RunRemoteStreamed(ctx context.Context, execFn func(ctx context.Context, script string) (stdout, stderr string, exitCode int, err error), cfg *config.ProjectConfig, name, dest, localWorkDir string, status iostream.StatusFunc, streams iostream.Streams) error {
	_, err := RunRemoteStreamedResult(ctx, execFn, cfg, name, dest, localWorkDir, status, streams)
	return err
}

// RunRemoteStreamedResult is RunRemoteResult for executors that stream output.
func RunRemoteStreamedResult(ctx context.Context, execFn func(ctx context.Context, script string) (stdout, stderr string, exitCode int, err error), cfg *config.ProjectConfig, name, dest, localWorkDir string, status iostream.StatusFunc, streams iostream.Streams) (Result, error) {
	return runRemote(ctx, execFn, cfg, name, dest, localWorkDir, status, streams, true)
}

func runRemote(ctx context.Context, execFn func(ctx context.Context, script string) (stdout, stderr string, exitCode int, err error), cfg *config.ProjectConfig, name, dest, localWorkDir string, status iostream.StatusFunc, streams iostream.Streams, outputStreamed bool) (Result, error) {
	commands := cfg.Commands
	if name != "" {
		c := cfg.FindCommand(name)
		if c == nil {
			return Result{}, fmt.Errorf("command %q not configured", name)
		}
		commands = []config.Command{*c}
	}
	maxWidth := commandutil.NameWidth(commands)
	for i, c := range commands {
		run := commandutil.ExpandCommandCtx(ctx, localWorkDir, c.Run)
		status(iostream.LevelInfo, "$ "+run)
		script := "cd " + shellEscape(dest) + " && " + run
		start := time.Now()
		stdout, stderr, exitCode, err := execFn(ctx, script)
		elapsed := time.Since(start)
		if err != nil {
			status(iostream.LevelError, fmt.Sprintf("%-*s  exec error", maxWidth, c.Name))
			commandutil.SkipRemaining(status, commands[i+1:], maxWidth)
			return Result{Passed: i, Total: len(commands)}, fmt.Errorf("remote %s: %w", c.Name, err)
		}
		if exitCode != 0 && (stdout != "" || stderr != "") {
			status(iostream.LevelInfo, c.Name+":")
		}
		if stdout != "" && !outputStreamed {
			_, _ = fmt.Fprint(streams.Out, stdout)
		}
		if stderr != "" && !outputStreamed {
			_, _ = fmt.Fprint(streams.Err, stderr)
		}
		if exitCode != 0 {
			status(iostream.LevelError, fmt.Sprintf("%-*s  %s", maxWidth, c.Name, commandutil.FormatElapsed(elapsed)))
			commandutil.SkipRemaining(status, commands[i+1:], maxWidth)
			return Result{Passed: i, Total: len(commands)}, &CheckFailedError{Name: c.Name, Remote: true, Code: exitCode}
		}
		status(iostream.LevelDone, fmt.Sprintf("%-*s  %s", maxWidth, c.Name, commandutil.FormatElapsed(elapsed)))
	}
	return Result{Passed: len(commands), Total: len(commands)}, nil
}

// RunRemoteInline runs a single inline command on a remote sidecar via SSH.
func RunRemoteInline(ctx context.Context, execFn func(ctx context.Context, script string) (stdout, stderr string, exitCode int, err error), name, command, dest string, status iostream.StatusFunc, streams iostream.Streams) error {
	return runRemoteInline(ctx, execFn, name, command, dest, status, streams, false)
}

// RunRemoteInlineStreamed runs an inline remote command whose executor has
// already written stdout and stderr to streams as they arrive.
func RunRemoteInlineStreamed(ctx context.Context, execFn func(ctx context.Context, script string) (stdout, stderr string, exitCode int, err error), name, command, dest string, status iostream.StatusFunc, streams iostream.Streams) error {
	return runRemoteInline(ctx, execFn, name, command, dest, status, streams, true)
}

func runRemoteInline(ctx context.Context, execFn func(ctx context.Context, script string) (stdout, stderr string, exitCode int, err error), name, command, dest string, status iostream.StatusFunc, streams iostream.Streams, outputStreamed bool) error {
	script := "cd " + shellEscape(dest) + " && " + command
	start := time.Now()
	stdout, stderr, exitCode, err := execFn(ctx, script)
	elapsed := time.Since(start)
	if err != nil {
		return fmt.Errorf("remote %s: %w", name, err)
	}
	if exitCode != 0 && (stdout != "" || stderr != "") {
		status(iostream.LevelInfo, name+":")
	}
	if stdout != "" && !outputStreamed {
		_, _ = fmt.Fprint(streams.Out, stdout)
	}
	if stderr != "" && !outputStreamed {
		_, _ = fmt.Fprint(streams.Err, stderr)
	}
	if exitCode != 0 {
		status(iostream.LevelError, fmt.Sprintf("%s  %s", name, commandutil.FormatElapsed(elapsed)))
		return &CheckFailedError{Name: name, Remote: true, Code: exitCode}
	}
	status(iostream.LevelDone, fmt.Sprintf("%s  %s", name, commandutil.FormatElapsed(elapsed)))
	return nil
}

// ExpandCommand replaces template variables in command before execution.
// {{CHANGED_PACKAGES}} expands to the space-separated list of Go package
// paths whose source files appear in `git diff HEAD`.
// Expands to "./..." when no .go files changed.
//
// Exported because every path that ships a configured command somewhere else to
// run must expand it first. Skipping expansion does not fail loudly: the literal
// {{CHANGED_PACKAGES}} reaches the shell, which exits non-zero for a reason that
// has nothing to do with the code under test.
func ExpandCommand(workDir, command string) string {
	return commandutil.ExpandCommand(workDir, command)
}

// ExpandCommandCtx is ExpandCommand with cancellation support for git queries.
func ExpandCommandCtx(ctx context.Context, workDir, command string) string {
	return commandutil.ExpandCommandCtx(ctx, workDir, command)
}

func runCommand(ctx context.Context, workDir, name, command string, timeoutSec, nameWidth int, envVars map[string]string, status iostream.StatusFunc, streams iostream.Streams) error {
	command = ExpandCommandCtx(ctx, workDir, command)
	status(iostream.LevelInfo, "$ "+command)

	if timeoutSec <= 0 {
		timeoutSec = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = workDir
	if len(envVars) > 0 {
		env := envctx.Environ(ctx)
		for k, v := range envVars {
			env = append(env, k+"="+v)
		}
		cmd.Env = env
	}
	cmd.Stdout = streams.Out
	cmd.Stderr = streams.Err

	start := time.Now()
	err := cmd.Run()
	elapsed := time.Since(start)

	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			status(iostream.LevelError, fmt.Sprintf("%-*s  timed out after %ds  %s", nameWidth, name, timeoutSec, formatElapsed(elapsed)))
			return fmt.Errorf("%s command timed out after %ds", name, timeoutSec)
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() != 0 {
			status(iostream.LevelError, fmt.Sprintf("%-*s  %s", nameWidth, name, formatElapsed(elapsed)))
			return &CheckFailedError{Name: name, Code: exitErr.ExitCode()}
		}
		return fmt.Errorf("%s: %w", name, err)
	}
	status(iostream.LevelDone, fmt.Sprintf("%-*s  %s", nameWidth, name, formatElapsed(elapsed)))
	return nil
}

// CheckFailedError reports a validate command that ran and exited non-zero:
// the project's own check failing, not chunk.
type CheckFailedError struct {
	Name   string
	Remote bool
	Code   int
}

func (e *CheckFailedError) Error() string {
	if e.Remote {
		return fmt.Sprintf("remote %s failed with exit code %d", e.Name, e.Code)
	}
	return fmt.Sprintf("%s command failed with exit code %d", e.Name, e.Code)
}

// TelemetryOutcome satisfies telemetry.Classified.
func (e *CheckFailedError) TelemetryOutcome() string { return telemetry.OutcomeCheckFailed }

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

	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
)

// Executor runs a single validation command.
type Executor interface {
	Execute(ctx context.Context, name, command string, timeoutSec int) error
}

// --- Local Executor ---

// LocalExecutor runs commands on the local machine via os/exec.
type LocalExecutor struct {
	workDir string
	streams iostream.Streams
}

// NewLocalExecutor creates an executor that runs commands via os/exec.
func NewLocalExecutor(workDir string, streams iostream.Streams) *LocalExecutor {
	return &LocalExecutor{workDir: workDir, streams: streams}
}

// Execute runs command in workDir with a sh -c wrapper.
// Template variables (e.g. {{CHANGED_PACKAGES}}) are expanded before execution.
func (e *LocalExecutor) Execute(ctx context.Context, name, command string, timeoutSec int) error {
	command = expandCommand(e.workDir, command)

	if timeoutSec <= 0 {
		timeoutSec = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = e.workDir
	cmd.Stdout = e.streams.Out
	cmd.Stderr = e.streams.Err

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

// --- Remote Executor ---

// RemoteExecutor runs commands on a remote sidecar via SSH.
type RemoteExecutor struct {
	execFn  func(context.Context, string) (stdout, stderr string, exitCode int, err error)
	dest    string
	streams iostream.Streams
}

// NewRemoteExecutor creates an executor that runs commands on a remote sidecar.
// execFn executes a raw script string on the remote. dest is the working
// directory on the remote sidecar; it is prepended as "cd <dest> && " to
// every command.
func NewRemoteExecutor(
	execFn func(context.Context, string) (string, string, int, error),
	dest string,
	streams iostream.Streams,
) *RemoteExecutor {
	return &RemoteExecutor{execFn: execFn, dest: dest, streams: streams}
}

// Execute runs command on the remote sidecar after cd-ing into dest.
func (e *RemoteExecutor) Execute(ctx context.Context, name, command string, _ int) error {
	script := "cd " + shellEscape(e.dest) + " && " + command
	stdout, stderr, exitCode, err := e.execFn(ctx, script)
	if err != nil {
		return fmt.Errorf("remote %s: %w", name, err)
	}
	if stdout != "" {
		_, _ = fmt.Fprint(e.streams.Out, stdout)
	}
	if stderr != "" {
		_, _ = fmt.Fprint(e.streams.Err, stderr)
	}
	if exitCode != 0 {
		return fmt.Errorf("remote %s failed with exit code %d", name, exitCode)
	}
	return nil
}

// ExecFn returns the raw execution function. Exposed for testing.
func (e *RemoteExecutor) ExecFn() func(context.Context, string) (string, string, int, error) {
	return e.execFn
}

// Dest returns the remote destination directory.
func (e *RemoteExecutor) Dest() string {
	return e.dest
}

// WorkspaceExists checks whether the remote workspace directory exists.
func (e *RemoteExecutor) WorkspaceExists(ctx context.Context) error {
	_, _, exitCode, err := e.execFn(ctx, "test -d "+shellEscape(e.dest))
	if err != nil {
		return err
	}
	if exitCode != 0 {
		return ErrWorkspaceNotFound
	}
	return nil
}

// WriteTo writes stdout and stderr to the given writers.
func (e *RemoteExecutor) WriteTo(stdout, stderr io.Writer) *RemoteExecutor {
	return &RemoteExecutor{
		execFn:  e.execFn,
		dest:    e.dest,
		streams: iostream.Streams{Out: stdout, Err: stderr},
	}
}

// shellEscape wraps arg in single quotes for safe use in a POSIX sh -c command.
func shellEscape(arg string) string {
	return "'" + strings.ReplaceAll(arg, "'", "'\\''") + "'"
}

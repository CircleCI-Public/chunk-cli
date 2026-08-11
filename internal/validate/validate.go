package validate

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
)

func formatElapsed(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return d.Round(time.Second).String()
}

// ErrNotConfigured indicates no validate commands are configured.
var ErrNotConfigured = errors.New("no validate commands configured")

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

func maxNameWidth(names []string) int {
	w := 0
	for _, n := range names {
		if len(n) > w {
			w = len(n)
		}
	}
	return w
}

func validateCommandNames(cmds []config.ValidateCommand) []string {
	names := make([]string, len(cmds))
	for i, c := range cmds {
		names[i] = c.Name
	}
	return names
}

func fixCommandNames(cmds []config.FixCommand) []string {
	names := make([]string, len(cmds))
	for i, c := range cmds {
		names[i] = c.Name
	}
	return names
}

func skipRemainingValidate(status iostream.StatusFunc, remaining []config.ValidateCommand, width int) {
	for _, c := range remaining {
		status(iostream.LevelWarn, fmt.Sprintf("%-*s  skipped", width, c.Name))
	}
}

// List prints all configured fix and validate command names and their run strings.
func List(cfg *config.ProjectConfig, status iostream.StatusFunc) error {
	if !cfg.HasCommands() {
		status(iostream.LevelInfo, "No commands configured.")
		status(iostream.LevelInfo, "Add commands with: chunk validate <name> --cmd \"your command\" --save")
		return nil
	}
	for _, c := range cfg.Fix {
		status(iostream.LevelInfo, fmt.Sprintf("%s (fix): %s", c.Name, c.Run))
	}
	for _, c := range cfg.Validate {
		status(iostream.LevelInfo, fmt.Sprintf("%s: %s", c.Name, c.Run))
	}
	return nil
}

// RunInline runs an inline command string locally.
func RunInline(ctx context.Context, workDir, name, command string, status iostream.StatusFunc, streams iostream.Streams) error {
	return runCommand(ctx, workDir, name, expandCommand(workDir, command), 0, 0, status, streams)
}

// RunFix runs all fix commands (or a single named one) locally.
// Fix commands never run on a remote sidecar.
func RunFix(ctx context.Context, workDir, name string, cmds []config.FixCommand, status iostream.StatusFunc, streams iostream.Streams) error {
	if len(cmds) == 0 {
		return nil
	}
	width := maxNameWidth(fixCommandNames(cmds))
	if name != "" {
		for _, c := range cmds {
			if c.Name == name {
				return runCommand(ctx, workDir, c.Name, c.Run, c.Timeout, 0, status, streams)
			}
		}
		return fmt.Errorf("fix command %q not configured", name)
	}
	for _, c := range cmds {
		if err := runCommand(ctx, workDir, c.Name, c.Run, c.Timeout, width, status, streams); err != nil {
			return err
		}
	}
	return nil
}

// RunAll runs all configured validate commands, stopping at the first failure.
func RunAll(ctx context.Context, workDir string, cmds []config.ValidateCommand, status iostream.StatusFunc, streams iostream.Streams) error {
	if len(cmds) == 0 {
		return ErrNotConfigured
	}
	width := maxNameWidth(validateCommandNames(cmds))
	for i, c := range cmds {
		if err := runCommand(ctx, workDir, c.Name, expandCommand(workDir, c.Run), c.Timeout, width, status, streams); err != nil {
			skipRemainingValidate(status, cmds[i+1:], width)
			return err
		}
	}
	return nil
}

// RunNamed runs a single named validate command.
func RunNamed(ctx context.Context, workDir, name string, cmds []config.ValidateCommand, status iostream.StatusFunc, streams iostream.Streams) error {
	for _, c := range cmds {
		if c.Name == name {
			return runCommand(ctx, workDir, c.Name, expandCommand(workDir, c.Run), c.Timeout, 0, status, streams)
		}
	}
	return fmt.Errorf("command %q not configured", name)
}

// RunDryRun prints validate commands without executing them.
func RunDryRun(cmds []config.ValidateCommand, name string, status iostream.StatusFunc) error {
	if len(cmds) == 0 {
		return ErrNotConfigured
	}
	if name != "" {
		for _, c := range cmds {
			if c.Name == name {
				status(iostream.LevelInfo, fmt.Sprintf("%s: %s", c.Name, c.Run))
				return nil
			}
		}
		return fmt.Errorf("command %q not configured", name)
	}
	for _, c := range cmds {
		status(iostream.LevelInfo, fmt.Sprintf("%s: %s", c.Name, c.Run))
	}
	return nil
}

// RunRemote runs validate commands on a remote sidecar via SSH.
// If name is non-empty, only the named command is run.
// workDir is the local repository root used to expand {{CHANGED_PACKAGES}}.
func RunRemote(ctx context.Context, execFn func(ctx context.Context, script string) (stdout, stderr string, exitCode int, err error), cmds []config.ValidateCommand, name, dest, workDir string, status iostream.StatusFunc, streams iostream.Streams) error {
	if name != "" {
		var target *config.ValidateCommand
		for i := range cmds {
			if cmds[i].Name == name {
				target = &cmds[i]
				break
			}
		}
		if target == nil {
			return fmt.Errorf("command %q not configured", name)
		}
		cmds = []config.ValidateCommand{*target}
	}

	width := maxNameWidth(validateCommandNames(cmds))
	for i, c := range cmds {
		run := expandCommand(workDir, c.Run)
		script := "cd " + shellEscape(dest) + " && " + run
		start := time.Now()
		stdout, stderr, exitCode, err := execFn(ctx, script)
		elapsed := time.Since(start)
		if err != nil {
			status(iostream.LevelError, fmt.Sprintf("%-*s  exec error", width, c.Name))
			skipRemainingValidate(status, cmds[i+1:], width)
			return fmt.Errorf("remote %s: %w", c.Name, err)
		}
		if exitCode != 0 && (stdout != "" || stderr != "") {
			status(iostream.LevelInfo, c.Name+":")
		}
		if stdout != "" {
			_, _ = fmt.Fprint(streams.Out, stdout)
		}
		if stderr != "" {
			_, _ = fmt.Fprint(streams.Err, stderr)
		}
		if exitCode != 0 {
			status(iostream.LevelError, fmt.Sprintf("%-*s  %s", width, c.Name, formatElapsed(elapsed)))
			skipRemainingValidate(status, cmds[i+1:], width)
			return fmt.Errorf("remote %s failed with exit code %d", c.Name, exitCode)
		}
		status(iostream.LevelDone, fmt.Sprintf("%-*s  %s", width, c.Name, formatElapsed(elapsed)))
	}
	return nil
}

// RunRemoteInline runs a single inline command on a remote sidecar via SSH.
func RunRemoteInline(ctx context.Context, execFn func(ctx context.Context, script string) (stdout, stderr string, exitCode int, err error), name, command, dest string, status iostream.StatusFunc, streams iostream.Streams) error {
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
	if stdout != "" {
		_, _ = fmt.Fprint(streams.Out, stdout)
	}
	if stderr != "" {
		_, _ = fmt.Fprint(streams.Err, stderr)
	}
	if exitCode != 0 {
		status(iostream.LevelError, fmt.Sprintf("%s  %s", name, formatElapsed(elapsed)))
		return fmt.Errorf("remote %s failed with exit code %d", name, exitCode)
	}
	status(iostream.LevelDone, fmt.Sprintf("%s  %s", name, formatElapsed(elapsed)))
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

func runCommand(ctx context.Context, workDir, name, command string, timeoutSec, nameWidth int, status iostream.StatusFunc, streams iostream.Streams) error {
	if timeoutSec <= 0 {
		timeoutSec = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = workDir
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
			return fmt.Errorf("%s command failed with exit code %d", name, exitErr.ExitCode())
		}
		return fmt.Errorf("%s: %w", name, err)
	}
	status(iostream.LevelDone, fmt.Sprintf("%-*s  %s", nameWidth, name, formatElapsed(elapsed)))
	return nil
}

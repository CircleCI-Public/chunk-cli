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

// RunAll runs all configured commands, stopping at the first failure.
func RunAll(ctx context.Context, workDir string, cfg *config.ProjectConfig, status iostream.StatusFunc, streams iostream.Streams) error {
	if !cfg.HasCommands() {
		return ErrNotConfigured
	}

	for i, c := range cfg.Commands {
		if err := runCommand(ctx, workDir, c.Name, c.Run, c.Timeout, status, streams); err != nil {
			for j := i + 1; j < len(cfg.Commands); j++ {
				status(iostream.LevelWarn, fmt.Sprintf("%s: skipped (%s failed)", cfg.Commands[j].Name, c.Name))
			}
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
		status(iostream.LevelInfo, fmt.Sprintf("%s: %s", c.Name, c.Run))
	}
	return nil
}

// RunRemote runs commands on a remote sandbox via SSH.
func RunRemote(ctx context.Context, exec func(ctx context.Context, script string) (stdout, stderr string, exitCode int, err error), cfg *config.ProjectConfig, dest string, streams iostream.Streams) error {
	for _, c := range cfg.Commands {
		script := "cd " + shellEscape(dest) + " && " + c.Run
		stdout, stderr, exitCode, err := exec(ctx, script)
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

// RunRemoteInline runs a single inline command on a remote sandbox via SSH.
func RunRemoteInline(ctx context.Context, exec func(ctx context.Context, script string) (stdout, stderr string, exitCode int, err error), name, command, dest string, streams iostream.Streams) error {
	script := "cd " + shellEscape(dest) + " && " + command
	stdout, stderr, exitCode, err := exec(ctx, script)
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
			return fmt.Errorf("%s command failed", name)
		}
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

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
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

// shellEscape wraps arg in single quotes for safe use in a POSIX sh -c command.
func shellEscape(arg string) string {
	return "'" + strings.ReplaceAll(arg, "'", "'\\''") + "'"
}

// DefaultTimeout is the per-command execution timeout in seconds.
const DefaultTimeout = 300

// List prints all configured command names and their run strings.
func List(cfg *config.ProjectConfig, streams iostream.Streams) error {
	if !cfg.HasCommands() {
		streams.Println(ui.Dim("No commands configured."))
		streams.Println(ui.Dim("Add commands with: chunk validate <name> --cmd \"your command\" --save"))
		return nil
	}
	for _, c := range cfg.Commands {
		streams.Printf("  %s: %s\n", ui.Bold(c.Name), ui.Gray(c.Run))
	}
	return nil
}

// RunInline runs an inline command string.
func RunInline(ctx context.Context, workDir, name, command string, streams iostream.Streams) error {
	return runCommand(ctx, workDir, name, command, 0, streams)
}

// RunNamed runs a single named command from config.
func RunNamed(ctx context.Context, workDir, name string, cfg *config.ProjectConfig, streams iostream.Streams) error {
	c := cfg.FindCommand(name)
	if c == nil {
		return fmt.Errorf("command %q not configured", name)
	}
	return runCommand(ctx, workDir, c.Name, c.Run, c.Timeout, streams)
}

// RunAll runs all configured commands, stopping at the first failure.
func RunAll(ctx context.Context, workDir string, cfg *config.ProjectConfig, streams iostream.Streams) error {
	if !cfg.HasCommands() {
		return fmt.Errorf("no validate commands configured, run 'chunk init' first")
	}

	for i, c := range cfg.Commands {
		if err := runCommand(ctx, workDir, c.Name, c.Run, c.Timeout, streams); err != nil {
			for j := i + 1; j < len(cfg.Commands); j++ {
				streams.ErrPrintf("%s: %s\n", ui.Bold(cfg.Commands[j].Name), ui.Yellow(fmt.Sprintf("skipped (%s failed)", c.Name)))
			}
			return err
		}
	}
	return nil
}

// RunDryRun prints commands without executing them.
func RunDryRun(cfg *config.ProjectConfig, name string, streams iostream.Streams) error {
	if !cfg.HasCommands() {
		return fmt.Errorf("no validate commands configured, run 'chunk init' first")
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
		streams.Printf("%s: %s\n", ui.Bold(c.Name), ui.Gray(c.Run))
	}
	return nil
}

// HasUncommittedChanges reports whether workDir has any staged, unstaged, or
// untracked changes. Returns false (no error) when not in a git repo or when
// there are no commits yet — both are treated as "nothing to validate".
// Uses `git status --porcelain` so newly created (untracked) files are detected.
func HasUncommittedChanges(workDir string) (bool, error) {
	out, err := exec.Command("git", "-C", workDir, "status", "--porcelain").Output()
	if err != nil {
		return false, nil
	}
	return len(strings.TrimSpace(string(out))) > 0, nil
}

// RunRemote runs commands on a remote sandbox via SSH.
func RunRemote(ctx context.Context, execFn func(ctx context.Context, script string) (stdout, stderr string, exitCode int, err error), cfg *config.ProjectConfig, dest string, streams iostream.Streams) error {
	for _, c := range cfg.Commands {
		script := "cd " + shellEscape(dest) + " && " + c.Run
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

func runCommand(ctx context.Context, workDir, name, command string, timeoutSec int, streams iostream.Streams) error {
	command = expandCommand(workDir, command)
	streams.ErrPrintf("%s %s\n", ui.Dim("Running "+name+":"), ui.Gray(command))

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

package validate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
)

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

// Status checks the cache for each command (or a single named command) and prints its status.
func Status(workDir, name string, cfg *config.ProjectConfig, status iostream.StatusFunc) error {
	commands := cfg.Commands
	if name != "" {
		c := cfg.FindCommand(name)
		if c == nil {
			return fmt.Errorf("command %q not configured", name)
		}
		commands = []config.Command{*c}
	}

	for _, c := range commands {
		cached := CheckCache(workDir, c.Name, c.FileExt)
		if cached != nil {
			level := iostream.LevelDone
			if cached.Status != statusPass {
				level = iostream.LevelWarn
			}
			status(level, fmt.Sprintf("%s: cached (%s)", c.Name, strings.ToUpper(cached.Status)))
		} else {
			status(iostream.LevelInfo, fmt.Sprintf("%s: no cached result", c.Name))
		}
	}
	return nil
}

// RunInline runs an inline command string, caching the result under the given name.
func RunInline(ctx context.Context, workDir, name, command string, force bool, status iostream.StatusFunc, streams iostream.Streams) error {
	if !force {
		if cached := CheckCache(workDir, name, ""); cached != nil {
			level := iostream.LevelDone
			if cached.Status != statusPass {
				level = iostream.LevelWarn
			}
			status(level, fmt.Sprintf("%s: cached (%s)", name, strings.ToUpper(cached.Status)))
			if cached.ExitCode != 0 {
				return fmt.Errorf("%s: cached failure", name)
			}
			return nil
		}
	}

	return runAndCache(ctx, workDir, name, command, "", 0, status, streams)
}

// RunNamed runs a single named command from config with caching.
func RunNamed(ctx context.Context, workDir, name string, force bool, cfg *config.ProjectConfig, status iostream.StatusFunc, streams iostream.Streams) error {
	c := cfg.FindCommand(name)
	if c == nil {
		return fmt.Errorf("command %q not configured", name)
	}

	if !force {
		if cached := CheckCache(workDir, c.Name, c.FileExt); cached != nil {
			level := iostream.LevelDone
			if cached.Status != statusPass {
				level = iostream.LevelWarn
			}
			status(level, fmt.Sprintf("%s: cached (%s)", c.Name, strings.ToUpper(cached.Status)))
			if cached.ExitCode != 0 {
				return fmt.Errorf("%s: cached failure", c.Name)
			}
			return nil
		}
	}

	return runAndCache(ctx, workDir, c.Name, c.Run, c.FileExt, c.Timeout, status, streams)
}

// RunAll runs all configured commands with optional cache bypass.
func RunAll(ctx context.Context, workDir string, force bool, cfg *config.ProjectConfig, status iostream.StatusFunc, streams iostream.Streams) error {
	if !cfg.HasCommands() {
		return fmt.Errorf("no validate commands configured, run 'chunk init' first")
	}

	for i, c := range cfg.Commands {
		if !force {
			if cached := CheckCache(workDir, c.Name, c.FileExt); cached != nil {
				level := iostream.LevelDone
				if cached.Status != statusPass {
					level = iostream.LevelWarn
				}
				status(level, fmt.Sprintf("%s: cached (%s)", c.Name, strings.ToUpper(cached.Status)))
				if cached.ExitCode != 0 {
					return fmt.Errorf("%s: cached failure", c.Name)
				}
				continue
			}
		}

		if err := runAndCache(ctx, workDir, c.Name, c.Run, c.FileExt, c.Timeout, status, streams); err != nil {
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

func runAndCache(ctx context.Context, workDir, name, command, fileExt string, timeoutSec int, status iostream.StatusFunc, streams iostream.Streams) error {
	status(iostream.LevelInfo, fmt.Sprintf("Running %s: %s", name, command))

	if timeoutSec <= 0 {
		timeoutSec = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = workDir

	var combined bytes.Buffer
	cmd.Stdout = io.MultiWriter(streams.Out, &combined)
	cmd.Stderr = io.MultiWriter(streams.Err, &combined)

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			_ = WriteCache(workDir, name, fileExt, 1, combined.String())
			return fmt.Errorf("%s command timed out after %ds", name, timeoutSec)
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 1
		}
	}

	_ = WriteCache(workDir, name, fileExt, exitCode, combined.String())

	if exitCode != 0 {
		return fmt.Errorf("%s command failed", name)
	}
	return nil
}

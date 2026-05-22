package validate

import (
	"context"
	"fmt"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
)

// Runner orchestrates the execution of validation commands.
type Runner struct {
	cfg      *config.ProjectConfig
	executor Executor
	status   iostream.StatusFunc
	streams  iostream.Streams
}

// NewRunner creates a runner that executes commands from cfg using executor.
// status is called before each command and on skips. streams receive command
// output.
func NewRunner(cfg *config.ProjectConfig, executor Executor, status iostream.StatusFunc, streams iostream.Streams) *Runner {
	return &Runner{
		cfg:      cfg,
		executor: executor,
		status:   status,
		streams:  streams,
	}
}

// RunAll runs every configured command, stopping at the first failure.
func (r *Runner) RunAll(ctx context.Context) error {
	if !r.cfg.HasCommands() {
		return ErrNotConfigured
	}

	for i, c := range r.cfg.Commands {
		r.status(iostream.LevelInfo, fmt.Sprintf("Running %s: %s", c.Name, c.Run))
		if err := r.executor.Execute(ctx, c.Name, c.Run, c.Timeout); err != nil {
			for j := i + 1; j < len(r.cfg.Commands); j++ {
				r.status(iostream.LevelWarn, fmt.Sprintf("%s: skipped (%s failed)", r.cfg.Commands[j].Name, c.Name))
			}
			return err
		}
	}
	return nil
}

// RunNamed runs a single named command from config.
func (r *Runner) RunNamed(ctx context.Context, name string) error {
	c := r.cfg.FindCommand(name)
	if c == nil {
		return fmt.Errorf("command %q not configured", name)
	}
	r.status(iostream.LevelInfo, fmt.Sprintf("Running %s: %s", c.Name, c.Run))
	return r.executor.Execute(ctx, c.Name, c.Run, c.Timeout)
}

// RunInline runs a single inline command string.
func (r *Runner) RunInline(ctx context.Context, name, command string) error {
	cmdName := name
	if cmdName == "" {
		cmdName = "custom"
	}
	r.status(iostream.LevelInfo, fmt.Sprintf("Running %s: %s", cmdName, command))
	return r.executor.Execute(ctx, cmdName, command, 0)
}

// DryRun prints commands without executing them.
func (r *Runner) DryRun(name string) error {
	if !r.cfg.HasCommands() {
		return ErrNotConfigured
	}

	commands := r.cfg.Commands
	if name != "" {
		c := r.cfg.FindCommand(name)
		if c == nil {
			return fmt.Errorf("command %q not configured", name)
		}
		commands = []config.Command{*c}
	}

	for _, c := range commands {
		r.status(iostream.LevelInfo, fmt.Sprintf("%s: %s", c.Name, c.Run))
	}
	return nil
}

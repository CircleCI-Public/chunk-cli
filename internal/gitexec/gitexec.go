// Package gitexec provides the low-level process boundary for local git commands.
package gitexec

import (
	"context"
	"os/exec"
)

// Runner executes git commands in Dir.
type Runner struct {
	Dir string
	Env []string
}

// Output runs git with args and returns its standard output.
func (r Runner) Output(ctx context.Context, args ...string) ([]byte, error) {
	return r.command(ctx, args...).Output()
}

// CombinedOutput runs git with args and returns its combined standard output
// and standard error.
func (r Runner) CombinedOutput(ctx context.Context, args ...string) ([]byte, error) {
	return r.command(ctx, args...).CombinedOutput()
}

// Run runs git with args.
func (r Runner) Run(ctx context.Context, args ...string) error {
	return r.command(ctx, args...).Run()
}

func (r Runner) command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = r.Dir
	if r.Env != nil {
		cmd.Env = r.Env
	}
	return cmd
}

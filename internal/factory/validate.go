package factory

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/review"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
)

// defaultCommandTimeout bounds a validation command with no timeout of its own.
const defaultCommandTimeout = 5 * time.Minute

// maxCommandOutput caps what is kept of one command's output; only its tail is
// fed back, but the cap stops a chatty test run growing memory without bound.
const maxCommandOutput = 256 * 1024

// ValidationCommands picks the configured commands the loop runs: those that
// check rather than fix, can run remotely, and need no template expanded
// against the developer's local changes.
func ValidationCommands(cmds []config.Command) []config.Command {
	var out []config.Command
	for _, c := range cmds {
		if c.Role == config.RoleAutofix || c.RunsLocally() || strings.Contains(c.Run, "{{") {
			continue
		}
		out = append(out, c)
	}
	return out
}

// runValidation runs each command in turn in the implementer's workspace, where
// the code under review already is, and returns one check per command. They
// run one after another because they share one checkout and one machine.
func runValidation(ctx context.Context, exec review.Execer, entry *sidecar.PoolEntry, cmds []config.Command, onDone func(Check)) []Check {
	checks := make([]Check, 0, len(cmds))
	for _, c := range cmds {
		check := runCommand(ctx, exec, entry, c)
		if onDone != nil {
			onDone(check)
		}
		checks = append(checks, check)
	}
	return checks
}

func runCommand(ctx context.Context, exec review.Execer, entry *sidecar.PoolEntry, c config.Command) Check {
	timeout := defaultCommandTimeout
	if c.Timeout > 0 {
		timeout = time.Duration(c.Timeout) * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	// Both streams go into one buffer, interleaved as they arrive, the way a
	// developer would see them in a terminal.
	var out strings.Builder
	script := fmt.Sprintf("cd %s && %s", sidecar.ShellEscape(entry.RepoPath), c.Run)
	code, err := exec(ctx, entry, script, nil, func(_ string, data []byte) {
		if room := maxCommandOutput - out.Len(); room > 0 {
			out.Write(data[:min(len(data), room)])
		}
	}, nil)
	if ctx.Err() == context.DeadlineExceeded {
		// A command that runs past its timeout is a failure the implementer may
		// be able to fix, such as a test that hangs, so it is fed back.
		return FromCommand(c.Name, entry.ID, -1, out.String()+fmt.Sprintf("\n(timed out after %s)", timeout), time.Since(start), nil)
	}
	return FromCommand(c.Name, entry.ID, code, out.String(), time.Since(start), err)
}

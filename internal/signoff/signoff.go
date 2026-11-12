// Package signoff runs a project's validation commands and reports each as a
// check run, so the result can be found again by the content it was run on.
package signoff

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/commandutil"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
)

// The outcomes a command is posted with.
const (
	Succeeded = "succeeded"
	Failed    = "failed"
	TimedOut  = "timed_out"
)

// ExecFunc runs a shell script and returns its output and exit
// code. An error means the result is unknown: the script may or may not have
// run, and how it ended is not known.
type ExecFunc func(ctx context.Context, script string) (stdout, stderr string, exitCode int, err error)

type Options struct {
	// Commands are run in order, each posted once.
	Commands []config.Command
	Key      string
	Exec     ExecFunc
	// Dir is the directory the commands run in.
	Dir  string
	Post func(ctx context.Context, run circleci.CheckRun) error
	// Now is the clock; time.Now when nil.
	Now func() time.Time
	// Status receives what is worth saying besides each command's line; it may
	// be nil.
	Status iostream.StatusFunc
	// Streams receives one line per command on Out.
	Streams iostream.Streams
}

type Result struct {
	Total int
	// Passed is how many succeeded; Failed how many failed or timed out.
	Passed, Failed int
	Posted         int
}

// Run runs each command in order and posts its outcome: exit 0
// is succeeded, a non-zero exit failed, and a command past its timeout
// timed_out. It never stops at a failure, so every command has a run.
//
// A command whose result is unknown, because exec or the transport failed,
// posts nothing and is returned in the error; later commands still run. A
// cancelled ctx stops Run before the interrupted command is posted and before
// any later one runs, and returns ctx's error. A failed post is likewise
// returned only once every command has run.
//
// A command that failed is not an error: it is in the result's Failed count.
func Run(ctx context.Context, opts Options) (Result, error) {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	status := opts.Status
	if status == nil {
		status = func(iostream.Level, string) {}
	}
	width := commandutil.NameWidth(opts.Commands)

	res := Result{Total: len(opts.Commands)}
	var errs []error
	for _, c := range opts.Commands {
		if err := ctx.Err(); err != nil {
			return res, errors.Join(append(errs, err)...)
		}
		status(iostream.LevelInfo, "$ "+c.Run)

		cmdCtx, cancel := ctx, context.CancelFunc(func() {})
		if c.Timeout > 0 {
			cmdCtx, cancel = context.WithTimeout(ctx, time.Duration(c.Timeout)*time.Second)
		}
		started := now()
		_, _, exitCode, err := opts.Exec(cmdCtx, "cd "+sidecar.ShellEscape(opts.Dir)+" && "+c.Run)
		ended := now()
		timedOut := cmdCtx.Err() == context.DeadlineExceeded && ctx.Err() == nil
		cancel()

		// Interrupted: the developer stopped signoff, not the command's work,
		// so what it was doing says nothing worth posting.
		if err := ctx.Err(); err != nil {
			return res, errors.Join(append(errs, err)...)
		}

		var outcome string
		switch {
		case timedOut:
			outcome = TimedOut
		case err != nil:
			status(iostream.LevelError, fmt.Sprintf("%-*s  exec error", width, c.Name))
			errs = append(errs, fmt.Errorf("run %s: %w", c.Name, err))
			continue
		case exitCode == 0:
			outcome = Succeeded
		default:
			outcome = Failed
		}
		if outcome == Succeeded {
			res.Passed++
		} else {
			res.Failed++
		}
		opts.Streams.Printf("  %-*s  %-9s  %s\n", width, c.Name, outcome, commandutil.FormatElapsed(ended.Sub(started)))

		run := circleci.CheckRun{
			Key: opts.Key, Name: c.Name, Outcome: outcome,
			StartedAt: started, EndedAt: ended,
		}
		if err := opts.Post(ctx, run); err != nil {
			errs = append(errs, fmt.Errorf("post %s: %w", c.Name, err))
			continue
		}
		res.Posted++
	}
	return res, errors.Join(errs...)
}

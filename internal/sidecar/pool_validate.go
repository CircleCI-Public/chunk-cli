package sidecar

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/commandutil"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
)

// DistributedRunOptions configures a validate-style remote pool execution.
type DistributedRunOptions struct {
	LocalWorkDir        string
	EnvVars             map[string]string
	Status              iostream.StatusFunc
	Streams             iostream.Streams
	FormatHeader        func(string) string
	FailFastUnavailable bool
}

// DistributedRunResult reports remote execution failures and commands that
// should fall back to local execution.
type DistributedRunResult struct {
	FellBack       []config.Command
	Err            error
	UnavailableErr error
}

// DistributedRunUnavailableError reports a fail-fast sidecar setup failure
// during pool-backed remote execution.
type DistributedRunUnavailableError struct {
	sidecarID  string
	code       string
	msg        string
	suggestion string
	exitCode   int
	err        error
}

func (e *DistributedRunUnavailableError) Error() string       { return e.err.Error() }
func (e *DistributedRunUnavailableError) Unwrap() error       { return e.err }
func (e *DistributedRunUnavailableError) SidecarID() string   { return e.sidecarID }
func (e *DistributedRunUnavailableError) ErrorCode() string   { return e.code }
func (e *DistributedRunUnavailableError) UserMessage() string { return e.msg }
func (e *DistributedRunUnavailableError) Suggestion() string  { return e.suggestion }
func (e *DistributedRunUnavailableError) UserExitCode() int   { return e.exitCode }

// RunCommands distributes remote validate commands across pool members.
// Commands assigned to one sidecar run sequentially; groups on distinct
// sidecars run in parallel. When a member cannot be reached or its workspace
// is missing, that group's commands are returned for local fallback.
func (p *Pool) RunCommands(ctx context.Context, cmds []config.Command, opts DistributedRunOptions) DistributedRunResult {
	if len(cmds) == 0 || len(p.entries) == 0 {
		return DistributedRunResult{}
	}

	groups := assignCommandsToPoolEntries(cmds, len(p.entries))
	type activeGroup struct {
		group []config.Command
		entry *PoolEntry
	}
	active := make([]activeGroup, 0, len(groups))
	for _, group := range groups {
		if len(group) == 0 {
			continue
		}
		entry, err := p.Acquire(ctx)
		if err != nil {
			for _, ag := range active {
				p.Release(ag.entry)
			}
			result := DistributedRunResult{}
			result.Err = fmt.Errorf("acquire sidecar: %w", err)
			return result
		}
		active = append(active, activeGroup{group: group, entry: entry})
		if opts.Status != nil {
			opts.Status(iostream.LevelInfo, fmt.Sprintf("running on sidecar %s: %s", entry.ID, poolCommandNames(group)))
		}
	}

	var result DistributedRunResult
	if len(active) <= 1 {
		for _, ag := range active {
			fb, cmdErr, unavailableErr := p.runCommandGroup(ctx, ag.entry, ag.group, opts.Status, opts.Streams, opts.LocalWorkDir, opts.EnvVars, opts.FailFastUnavailable)
			result.FellBack = append(result.FellBack, fb...)
			result.Err = errors.Join(result.Err, cmdErr)
			result.UnavailableErr = errors.Join(result.UnavailableErr, unavailableErr)
			p.Release(ag.entry)
		}
		return result
	}

	type groupResult struct {
		group          []config.Command
		entry          *PoolEntry
		stdout         string
		stderr         string
		fellBack       []config.Command
		cmdErr         error
		unavailableErr error
	}

	results := make([]groupResult, len(active))
	var wg sync.WaitGroup
	for idx, ag := range active {
		wg.Add(1)
		go func(idx int, ag activeGroup) {
			defer wg.Done()
			defer p.Release(ag.entry)
			var outBuf, errBuf strings.Builder
			buf := iostream.Streams{Out: &outBuf, Err: &errBuf}
			fb, cmdErr, unavailableErr := p.runCommandGroup(ctx, ag.entry, ag.group, func(iostream.Level, string) {}, buf, opts.LocalWorkDir, opts.EnvVars, opts.FailFastUnavailable)
			results[idx] = groupResult{group: ag.group, entry: ag.entry, stdout: outBuf.String(), stderr: errBuf.String(), fellBack: fb, cmdErr: cmdErr, unavailableErr: unavailableErr}
		}(idx, ag)
	}
	wg.Wait()

	for _, r := range results {
		header := poolCommandNames(r.group)
		if r.stdout != "" || r.stderr != "" {
			if opts.FormatHeader != nil {
				opts.Streams.ErrPrintln(opts.FormatHeader(header + ":"))
			} else {
				opts.Streams.ErrPrintln(header + ":")
			}
		}
		if r.stdout != "" {
			_, _ = fmt.Fprint(opts.Streams.Out, r.stdout)
		}
		if r.stderr != "" {
			_, _ = fmt.Fprint(opts.Streams.Err, r.stderr)
		}
		result.FellBack = append(result.FellBack, r.fellBack...)
		result.UnavailableErr = errors.Join(result.UnavailableErr, r.unavailableErr)
		if r.cmdErr != nil {
			if opts.Status != nil {
				opts.Status(iostream.LevelWarn, fmt.Sprintf("%s failed: %v", header, r.cmdErr))
			}
			result.Err = errors.Join(result.Err, r.cmdErr)
		} else if len(r.fellBack) == 0 && opts.Status != nil {
			opts.Status(iostream.LevelDone, fmt.Sprintf("%s passed", header))
		}
	}

	return result
}

func (p *Pool) runCommandGroup(ctx context.Context, entry *PoolEntry, cmds []config.Command, statusFn iostream.StatusFunc, streams iostream.Streams, localWorkDir string, envVars map[string]string, failFastUnavailable bool) (fellBack []config.Command, cmdErr, unavailableErr error) {
	target := Target{
		Client:    entry.Client,
		SidecarID: entry.ID,
		Workdir:   entry.RepoPath,
	}
	execFn, dest, err := target.ReadyExecRunner(ctx, localWorkDir, envVars, streams)
	if err != nil {
		var wsErr *WorkspaceNotFoundError
		if errors.As(err, &wsErr) {
			if failFastUnavailable {
				return nil, nil, &DistributedRunUnavailableError{
					sidecarID:  wsErr.SidecarID,
					code:       "sidecar.workspace_missing",
					msg:        fmt.Sprintf("Workspace not found on newly created sidecar %s.", wsErr.SidecarID),
					suggestion: "Run 'chunk sidecar env build' to prepare the workspace.",
					exitCode:   5,
					err:        err,
				}
			}
			streams.ErrPrintf("warning: %v; run 'chunk sidecar env build' to set up the workspace; running %s locally instead\n", err, poolCommandNames(cmds))
			return cmds, nil, nil
		}
		if failFastUnavailable {
			sidecarID := entry.ID
			var unavailableErr *TargetUnavailableError
			if errors.As(err, &unavailableErr) && unavailableErr.SidecarID != "" {
				sidecarID = unavailableErr.SidecarID
			}
			return nil, nil, &DistributedRunUnavailableError{
				sidecarID:  sidecarID,
				code:       "sidecar.unreachable",
				msg:        fmt.Sprintf("Could not reach newly created sidecar %s.", sidecarID),
				suggestion: "The sidecar may still be starting. Try again in a moment.",
				exitCode:   4,
				err:        err,
			}
		}
		streams.ErrPrintf("warning: could not reach sidecar (%v); running %s locally instead\n", err, poolCommandNames(cmds))
		return cmds, nil, nil
	}
	groupCfg := &config.ProjectConfig{Commands: cmds}
	return nil, RunRemoteCommands(ctx, execFn, groupCfg, "", dest, localWorkDir, statusFn, streams), nil
}

func assignCommandsToPoolEntries(cmds []config.Command, n int) [][]config.Command {
	groups := make([][]config.Command, n)
	for i, c := range cmds {
		k := i % n
		groups[k] = append(groups[k], c)
	}
	return groups
}

func poolCommandNames(cmds []config.Command) string {
	names := make([]string, len(cmds))
	for i, c := range cmds {
		names[i] = c.Name
	}
	return strings.Join(names, ", ")
}

// RunRemoteCommands executes configured commands on a remote sidecar runner.
// If name is non-empty, only the named command is run.
func RunRemoteCommands(ctx context.Context, execFn func(context.Context, string) (stdout, stderr string, exitCode int, err error), cfg *config.ProjectConfig, name, dest, localWorkDir string, status iostream.StatusFunc, streams iostream.Streams) error {
	commands := cfg.Commands
	if name != "" {
		c := cfg.FindCommand(name)
		if c == nil {
			return fmt.Errorf("command %q not configured", name)
		}
		commands = []config.Command{*c}
	}
	maxWidth := commandutil.NameWidth(commands)
	for i, c := range commands {
		run := commandutil.ExpandCommand(localWorkDir, c.Run)
		script := "cd " + ShellEscape(dest) + " && " + run
		start := time.Now()
		stdout, stderr, exitCode, err := execFn(ctx, script)
		elapsed := time.Since(start)
		if err != nil {
			status(iostream.LevelError, fmt.Sprintf("%-*s  exec error", maxWidth, c.Name))
			commandutil.SkipRemaining(status, commands[i+1:], maxWidth)
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
			status(iostream.LevelError, fmt.Sprintf("%-*s  %s", maxWidth, c.Name, commandutil.FormatElapsed(elapsed)))
			commandutil.SkipRemaining(status, commands[i+1:], maxWidth)
			return fmt.Errorf("remote %s failed with exit code %d", c.Name, exitCode)
		}
		status(iostream.LevelDone, fmt.Sprintf("%-*s  %s", maxWidth, c.Name, commandutil.FormatElapsed(elapsed)))
	}
	return nil
}

// RunRemoteInline executes a single inline command on a remote sidecar runner.
func RunRemoteInline(ctx context.Context, execFn func(context.Context, string) (stdout, stderr string, exitCode int, err error), name, command, dest string, status iostream.StatusFunc, streams iostream.Streams) error {
	script := "cd " + ShellEscape(dest) + " && " + command
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
		status(iostream.LevelError, fmt.Sprintf("%s  %s", name, commandutil.FormatElapsed(elapsed)))
		return fmt.Errorf("remote %s failed with exit code %d", name, exitCode)
	}
	status(iostream.LevelDone, fmt.Sprintf("%s  %s", name, commandutil.FormatElapsed(elapsed)))
	return nil
}

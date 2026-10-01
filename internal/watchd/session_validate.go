package watchd

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

// maxCommandOutput caps what is kept of one command's output while it runs.
const maxCommandOutput = 256 * 1024

// commandTail is how much of a failed command's output the implementer is
// shown. The end is kept: that is where test runners and compilers summarise a
// failure.
const commandTail = 4000

// validationCommands picks the configured commands a session runs as checks:
// those that check rather than fix, can run remotely, and need no template
// expanded against the developer's local changes.
func validationCommands(cmds []config.Command) []config.Command {
	var out []config.Command
	for _, c := range cmds {
		if c.Role == "autofix" || c.Local || strings.Contains(c.Run, "{{") {
			continue
		}
		out = append(out, c)
	}
	return out
}

// runValidation runs each validation command in turn on the implementer's
// sidecar, where the work already is, and returns one result per command. They
// run one after another because they share one checkout and one machine. A
// command that exits non-zero gives one finding, high severity, carrying its
// output: that is what the implementer is asked to fix.
func (d *daemon) runValidation(ctx context.Context, entry *sessionEntry, ridx int, plan *sessionPlan, pe *sidecar.PoolEntry) []ReviewResult {
	number := entry.roundNumber(ridx)
	results := make([]ReviewResult, 0, len(plan.commands))
	for _, c := range plan.commands {
		label := fmt.Sprintf("round %d validate: %s", number, c.Name)
		exec := d.execerFor(plan.root, func(string, string) string { return label })
		entry.updateCheck(ridx, c.Name, func(p *ReviewPrompt) { p.State, p.SidecarID = PromptRunning, pe.ID })
		res := runCommand(ctx, exec, pe, c, func(id string) {
			entry.updateCheck(ridx, c.Name, func(p *ReviewPrompt) { p.CommandID = id })
		})
		entry.updateCheck(ridx, c.Name, func(p *ReviewPrompt) {
			p.State, p.Error, p.DurationMS, p.Findings = PromptDone, res.Error, res.DurationMS, len(res.Findings)
			if res.Error != "" {
				p.State = PromptFailed
			}
		})
		results = append(results, res)
	}
	return results
}

func runCommand(ctx context.Context, exec review.Execer, pe *sidecar.PoolEntry, c config.Command, onSubmitted func(string)) ReviewResult {
	timeout := defaultCommandTimeout
	if c.Timeout > 0 {
		timeout = time.Duration(c.Timeout) * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	res := ReviewResult{Prompt: c.Name, Kind: CheckValidate, SidecarID: pe.ID}
	start := time.Now()
	// Both streams go into one buffer, interleaved as they arrive, the way a
	// developer would see them in a terminal.
	var out strings.Builder
	script := fmt.Sprintf("cd %s && %s", sidecar.ShellEscape(pe.RepoPath), c.Run)
	code, err := exec(cctx, pe, script, nil, func(_ string, data []byte) {
		if room := maxCommandOutput - out.Len(); room > 0 {
			out.Write(data[:min(len(data), room)])
		}
	}, onSubmitted)
	res.DurationMS = time.Since(start).Milliseconds()
	output := strings.TrimSpace(out.String())
	res.Output = tailText(output, commandTail)

	failure := ""
	switch {
	case cctx.Err() == context.DeadlineExceeded && ctx.Err() == nil:
		// A command that runs past its timeout is a failure the implementer may
		// be able to fix, such as a test that hangs.
		failure = fmt.Sprintf("timed out after %s", timeout)
	case err != nil:
		res.Error = err.Error()
		return res
	case code != 0:
		failure = fmt.Sprintf("exited %d", code)
	default:
		return res
	}
	res.Findings = []review.Finding{{
		ID:       "validate:" + c.Name,
		Prompt:   c.Name,
		Severity: review.SeverityHigh,
		Body:     fmt.Sprintf("The validation command %q (`%s`) %s.\n```\n%s\n```", c.Name, c.Run, failure, tailText(output, commandTail)),
	}}
	return res
}

func tailText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := len(s) - n
	for cut < len(s) && (s[cut]&0xC0) == 0x80 {
		cut++
	}
	return "…" + s[cut:]
}

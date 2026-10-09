package cmd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/gitremote"
	"github.com/CircleCI-Public/chunk-cli/internal/gitutil"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
)

// sessionPollInterval is how often `chunk factory` asks the daemon how its
// run is going.
const sessionPollInterval = time.Second

// maxSessionPollFailures is how many polls in a row may fail before
// `chunk factory` gives up following its run. The run is on the daemon and
// keeps going either way.
const maxSessionPollFailures = 5

// requireLocalDaemon refuses to run when the environment points at a remote
// daemon. A factory run works from files on this machine, so it belongs to the
// local daemon; a remote daemon would be working on a different checkout.
func requireLocalDaemon() error {
	if chunkd.CurrentConnection().Remote == "" {
		return nil
	}
	return newUserError("chunk factory runs on the local chunk daemon, but CHUNK_DAEMON_REMOTE_ADDR is set.").
		withCode("command.invalid_args").
		withSuggestion("Unset CHUNK_DAEMON_REMOTE_ADDR to use this command.").
		withExitCode(ExitBadArgs).
		withoutDetail()
}

// sessionProjectRoot names the project to work on: the git repository
// containing the working directory.
func sessionProjectRoot(ctx context.Context) (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("determine working directory: %w", err)
	}
	top := gitutil.TopLevelCtx(ctx, dir)
	if top == "" {
		return "", newUserError(fmt.Sprintf("%s is not inside a git repository.", dir)).
			withCode("command.invalid_args").
			withExitCode(ExitBadArgs).
			withoutDetail()
	}
	// The sandbox pool works out which repository to clone from the origin
	// remote. Without one it fails deep inside pool setup with a git exit code,
	// so say so here, before anything is started.
	if _, err := gitremote.URL(ctx, top, "origin"); err != nil {
		return "", newUserError("This project has no git remote named origin.").
			withSuggestion("Add one with: git remote add origin <url>").
			withCode("command.invalid_args").
			withExitCode(ExitBadArgs).
			wrap(err)
	}
	// The daemon adopts a project it has not seen when asked to work on it, so
	// there is nothing to register here.
	return config.CanonicalProjectRoot(top), nil
}

// sessionError renders a failure talking to the session API.
func sessionError(err error) error {
	var refused *chunkd.SessionRefused
	switch {
	case errors.Is(err, chunkd.ErrDaemonUnavailable):
		return newUserError("The chunk daemon is not reachable.").
			withSuggestion("Start it with 'chunk watch'.").
			wrap(err)
	case errors.As(err, &refused):
		e := newUserError(refused.Message).withoutDetail()
		switch refused.Status {
		case http.StatusConflict:
			e = e.withSuggestion("Follow the active one in 'chunk watch' (press r).")
		case http.StatusNotFound:
			e = e.withSuggestion("The daemon tracks projects it has seen; check the path or the session ID.")
		}
		return e
	}
	return fmt.Errorf("talk to the chunk daemon: %w", err)
}

// printLostFactoryWork says where a factory run was working when its session
// can no longer be followed. The daemon still commits the work as it stops.
func printLostFactoryWork(streams iostream.Streams, f *chunkd.FactoryRun) {
	if f == nil {
		return
	}
	streams.ErrPrintf("The factory run was working in %s on %s; the daemon commits what it did there as it stops.\n  %s\n",
		f.Worktree, f.Branch, keepWorkHint(f))
}

// followSession polls a factory session, reporting what changes, until it
// ends. Ctrl-C cancels it and follows it until it has wound down.
func followSession(ctx context.Context, streams iostream.Streams, id string, jsonOut bool) error {
	rep := newSessionReporter(newStatusFunc(streams))
	failures := 0
	ticker := time.NewTicker(sessionPollInterval)
	defer ticker.Stop()
	// lastFactory is where a factory run's work was last seen, so it can still
	// be pointed to if the daemon goes away: its record goes with it.
	var lastFactory *chunkd.FactoryRun
	for {
		detail, err := chunkd.FetchSession(id)
		var refused *chunkd.SessionRefused
		switch {
		case errors.As(err, &refused):
			printLostFactoryWork(streams, lastFactory)
			return sessionError(err)
		case err != nil:
			if failures++; failures >= maxSessionPollFailures {
				printLostFactoryWork(streams, lastFactory)
				return newUserError("Lost contact with the chunk daemon.").
					withSuggestion(fmt.Sprintf("Run %s may still be running on the daemon. Follow it in 'chunk watch' (press r).", id)).
					wrap(err)
			}
		default:
			failures = 0
			if detail.Factory != nil && detail.Factory.Worktree != "" {
				lastFactory = detail.Factory
			}
			rep.report(detail)
			if detail.State.Finished() {
				return finishFactory(streams, detail, jsonOut)
			}
		}
		select {
		case <-ctx.Done():
			if err := chunkd.CancelSession(id); err != nil {
				return sessionError(err)
			}
			streams.ErrPrintf("Stopping %s...\n", id)
			// Only one interrupt: from here the session is followed until it
			// ends, which is when its work has been put away.
			ctx = context.WithoutCancel(ctx)
		case <-ticker.C:
		}
	}
}

func roundNote(r chunkd.Round) string {
	if r.Note == "" {
		return ""
	}
	return " — " + r.Note
}

// sessionReporter turns successive snapshots of a session into status lines.
type sessionReporter struct {
	status  iostream.StatusFunc
	rounds  map[int]chunkd.RoundState
	reviews map[string]chunkd.PromptRunState
	// implement and checks are what has been said of a factory round's
	// implementer turn and how many of its validation commands.
	implement map[int]chunkd.RoundImplement
	checks    map[int]int
	// progress counts the lines of the factory run's progress already said.
	progress int
	// checked marks the factory rounds whose review findings have been said.
	checked map[int]bool
}

func newSessionReporter(status iostream.StatusFunc) *sessionReporter {
	return &sessionReporter{
		status:    status,
		rounds:    map[int]chunkd.RoundState{},
		reviews:   map[string]chunkd.PromptRunState{},
		implement: map[int]chunkd.RoundImplement{},
		checks:    map[int]int{},
		checked:   map[int]bool{},
	}
}

func (r *sessionReporter) report(d chunkd.SessionDetail) {
	s := d.Session
	// A factory run's progress comes first: most of it is the setup before
	// any round, which a snapshot caught up on all at once would otherwise
	// show after the rounds.
	if f := s.Factory; f != nil {
		for _, l := range f.Progress.Since(r.progress) {
			r.status(l.Level.Level(), l.Text)
		}
		r.progress = f.Progress.Total
	}
	for i, round := range s.Rounds {
		if r.rounds[i] != round.State {
			r.rounds[i] = round.State
			// A round's note says how it went, which nothing else does.
			number := strconv.Itoa(round.Number)
			if s.Factory != nil && s.Factory.Attempts > 0 {
				number += "/" + strconv.Itoa(s.Factory.Attempts)
			}
			r.status(iostream.LevelStep, fmt.Sprintf("round %s: %s%s", number, round.State, roundNote(round)))
		}
		if impl := round.Implement; impl != nil {
			r.reportImplement(i, *impl)
		}
		for _, c := range round.Checks[min(r.checks[i], len(round.Checks)):] {
			r.reportCheck(c)
		}
		r.checks[i] = len(round.Checks)
		for _, p := range round.Reviews {
			key := fmt.Sprintf("%d/%s", i, p.Name)
			if r.reviews[key] == p.State {
				continue
			}
			r.reviews[key] = p.State
			switch p.State {
			case chunkd.PromptRunning:
				r.status(iostream.LevelInfo, fmt.Sprintf("reviewing %s on %s", p.Name, p.SidecarID))
			case chunkd.PromptDone:
				r.status(iostream.LevelDone, fmt.Sprintf("%s reviewed in %s", p.Name, msDuration(p.DurationMS)))
			case chunkd.PromptFailed:
				r.status(iostream.LevelWarn, fmt.Sprintf("%s: %s", p.Name, p.Error))
			case chunkd.PromptQueued:
				// Nothing to say yet.
			}
		}
		// A factory round's findings are recorded as it is checked, which is
		// when it is done.
		if round.State == chunkd.RoundDone && !r.checked[i] {
			r.checked[i] = true
			r.reportFindings(roundResults(d, round.Number))
		}
	}
}

// reportImplement says how a factory round's implementer turn went, once it
// has gone somewhere new.
func (r *sessionReporter) reportImplement(i int, impl chunkd.RoundImplement) {
	prev := r.implement[i]
	r.implement[i] = impl
	if impl.State != prev.State {
		switch impl.State {
		case chunkd.ImplementApplied:
			r.status(iostream.LevelDone, fmt.Sprintf("implementer finished in %s ($%.2f)", msDuration(impl.DurationMS), impl.CostUSD))
			if impl.Summary != "" {
				r.status(iostream.LevelInfo, oneLineSummary(impl.Summary))
			}
		case chunkd.ImplementEmpty:
			r.status(iostream.LevelWarn, "no changes")
		case chunkd.ImplementFailed:
			r.status(iostream.LevelWarn, "implementer failed: "+impl.Error)
		case chunkd.ImplementRunning:
			// The round's own state line says so.
		}
	}
	if impl.Stat != "" && impl.Stat != prev.Stat {
		r.status(iostream.LevelInfo, impl.Stat)
	}
}

// roundResults is the review results of the round numbered n.
func roundResults(d chunkd.SessionDetail, n int) []chunkd.ReviewResult {
	for _, rd := range d.Details {
		if rd.Number == n {
			return rd.Results
		}
	}
	return nil
}

// reportFindings says what each of a factory round's reviews found: what the
// implementer is asked to fix next, or what is left when the run ends.
func (r *sessionReporter) reportFindings(results []chunkd.ReviewResult) {
	for _, res := range results {
		switch res.Status {
		case chunkd.CheckPassed:
			r.status(iostream.LevelDone, fmt.Sprintf("review %s: no findings worth changing", res.Prompt))
		case chunkd.CheckFailed:
			r.status(iostream.LevelError, fmt.Sprintf("review %s: %d finding(s)", res.Prompt, len(res.Findings)))
		case chunkd.CheckErrored:
			r.status(iostream.LevelWarn, fmt.Sprintf("review %s could not run: %s", res.Prompt, res.Error))
			continue
		}
		for _, f := range res.Findings {
			r.status(iostream.LevelInfo, fmt.Sprintf("  [%s] %s %s", f.Severity, f.Location(), oneLineSummary(f.Body)))
		}
	}
}

// failedOutputLines is how much of a failed validation command's output is
// shown: enough for a test runner's or compiler's summary.
const failedOutputLines = 20

// reportCheck says how a factory round's validation command came out.
func (r *sessionReporter) reportCheck(c chunkd.RoundCheck) {
	switch c.Status {
	case chunkd.CheckPassed:
		r.status(iostream.LevelDone, fmt.Sprintf("  %s passed in %s", c.Name, msDuration(c.DurationMS)))
	case chunkd.CheckFailed:
		r.status(iostream.LevelError, fmt.Sprintf("  %s failed in %s", c.Name, msDuration(c.DurationMS)))
		if c.Output == "" {
			return
		}
		lines := strings.Split(c.Output, "\n")
		for _, l := range lines[max(len(lines)-failedOutputLines, 0):] {
			r.status(iostream.LevelInfo, "    "+l)
		}
	case chunkd.CheckErrored:
		r.status(iostream.LevelWarn, fmt.Sprintf("  %s could not run: %s", c.Name, c.Error))
	}
}

func msDuration(ms int64) time.Duration {
	return (time.Duration(ms) * time.Millisecond).Round(time.Second)
}

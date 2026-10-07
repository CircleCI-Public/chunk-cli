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

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/gitremote"
	"github.com/CircleCI-Public/chunk-cli/internal/gitutil"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/watchd"
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
	if watchd.CurrentConnection().Remote == "" {
		return nil
	}
	return newUserError("chunk factory runs on the local watch daemon, but CHUNK_WATCHD_REMOTE_ADDR is set.").
		withCode("command.invalid_args").
		withSuggestion("Unset CHUNK_WATCHD_REMOTE_ADDR to use this command.").
		withExitCode(ExitBadArgs).
		withoutDetail()
}

// sessionProjectRoot names the project to work on: the git repository
// containing the working directory, registered so the daemon can find it.
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
	// so say so here, before anything is registered or started.
	if _, err := gitremote.URL(ctx, top, "origin"); err != nil {
		return "", newUserError("This project has no git remote named origin.").
			withSuggestion("Add one with: git remote add origin <url>").
			withCode("command.invalid_args").
			withExitCode(ExitBadArgs).
			wrap(err)
	}
	root := config.CanonicalProjectRoot(top)
	dataDir, err := config.ProjectDataDir(root)
	if err != nil {
		return "", fmt.Errorf("data dir for %s: %w", root, err)
	}
	// Registration is how the daemon learns a project exists.
	if err := sidecar.RegisterProjectRoot(dataDir, root); err != nil {
		return "", fmt.Errorf("register project %s: %w", root, err)
	}
	return root, nil
}

// sessionError renders a failure talking to the session API.
func sessionError(err error) error {
	var refused *watchd.SessionRefused
	switch {
	case errors.Is(err, watchd.ErrDaemonUnavailable):
		return newUserError("The watch daemon is not reachable.").
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
	return fmt.Errorf("talk to the watch daemon: %w", err)
}

// printLostFactoryWork says where a factory run was working when its session
// can no longer be followed. The daemon still commits the work as it stops.
func printLostFactoryWork(streams iostream.Streams, f *watchd.FactoryRun) {
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
	var lastFactory *watchd.FactoryRun
	for {
		detail, err := watchd.FetchSession(id)
		var refused *watchd.SessionRefused
		switch {
		case errors.As(err, &refused):
			printLostFactoryWork(streams, lastFactory)
			return sessionError(err)
		case err != nil:
			if failures++; failures >= maxSessionPollFailures {
				printLostFactoryWork(streams, lastFactory)
				return newUserError("Lost contact with the watch daemon.").
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
			if err := watchd.CancelSession(id); err != nil {
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

func roundNote(r watchd.Round) string {
	if r.Note == "" {
		return ""
	}
	return " — " + r.Note
}

// sessionReporter turns successive snapshots of a session into status lines.
type sessionReporter struct {
	status  iostream.StatusFunc
	rounds  map[int]watchd.RoundState
	reviews map[string]watchd.PromptRunState
	// implement and checks are what has been said of a factory round's
	// implementer turn and how many of its validation commands.
	implement map[int]watchd.RoundImplement
	checks    map[int]int
	// progress counts the lines of the factory run's progress already said.
	progress int
	// checked marks the factory rounds whose review findings have been said.
	checked map[int]bool
}

func newSessionReporter(status iostream.StatusFunc) *sessionReporter {
	return &sessionReporter{
		status:    status,
		rounds:    map[int]watchd.RoundState{},
		reviews:   map[string]watchd.PromptRunState{},
		implement: map[int]watchd.RoundImplement{},
		checks:    map[int]int{},
		checked:   map[int]bool{},
	}
}

func (r *sessionReporter) report(d watchd.SessionDetail) {
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
			case watchd.PromptRunning:
				r.status(iostream.LevelInfo, fmt.Sprintf("reviewing %s on %s", p.Name, p.SidecarID))
			case watchd.PromptDone:
				r.status(iostream.LevelDone, fmt.Sprintf("%s reviewed in %s", p.Name, msDuration(p.DurationMS)))
			case watchd.PromptFailed:
				r.status(iostream.LevelWarn, fmt.Sprintf("%s: %s", p.Name, p.Error))
			case watchd.PromptQueued:
				// Nothing to say yet.
			}
		}
		// A factory round's findings are recorded as it is checked, which is
		// when it is done.
		if round.State == watchd.RoundDone && !r.checked[i] {
			r.checked[i] = true
			r.reportFindings(roundResults(d, round.Number))
		}
	}
}

// reportImplement says how a factory round's implementer turn went, once it
// has gone somewhere new.
func (r *sessionReporter) reportImplement(i int, impl watchd.RoundImplement) {
	prev := r.implement[i]
	r.implement[i] = impl
	if impl.State != prev.State {
		switch impl.State {
		case watchd.ImplementApplied:
			r.status(iostream.LevelDone, fmt.Sprintf("implementer finished in %s ($%.2f)", msDuration(impl.DurationMS), impl.CostUSD))
			if impl.Summary != "" {
				r.status(iostream.LevelInfo, oneLineSummary(impl.Summary))
			}
		case watchd.ImplementEmpty:
			r.status(iostream.LevelWarn, "no changes")
		case watchd.ImplementFailed:
			r.status(iostream.LevelWarn, "implementer failed: "+impl.Error)
		case watchd.ImplementRunning:
			// The round's own state line says so.
		}
	}
	if impl.Stat != "" && impl.Stat != prev.Stat {
		r.status(iostream.LevelInfo, impl.Stat)
	}
}

// roundResults is the review results of the round numbered n.
func roundResults(d watchd.SessionDetail, n int) []watchd.ReviewResult {
	for _, rd := range d.Details {
		if rd.Number == n {
			return rd.Results
		}
	}
	return nil
}

// reportFindings says what each of a factory round's reviews found: what the
// implementer is asked to fix next, or what is left when the run ends.
func (r *sessionReporter) reportFindings(results []watchd.ReviewResult) {
	for _, res := range results {
		switch res.Status {
		case watchd.CheckPassed:
			r.status(iostream.LevelDone, fmt.Sprintf("review %s: no findings worth changing", res.Prompt))
		case watchd.CheckFailed:
			r.status(iostream.LevelError, fmt.Sprintf("review %s: %d finding(s)", res.Prompt, len(res.Findings)))
		case watchd.CheckErrored:
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
func (r *sessionReporter) reportCheck(c watchd.RoundCheck) {
	switch c.Status {
	case watchd.CheckPassed:
		r.status(iostream.LevelDone, fmt.Sprintf("  %s passed in %s", c.Name, msDuration(c.DurationMS)))
	case watchd.CheckFailed:
		r.status(iostream.LevelError, fmt.Sprintf("  %s failed in %s", c.Name, msDuration(c.DurationMS)))
		if c.Output == "" {
			return
		}
		lines := strings.Split(c.Output, "\n")
		for _, l := range lines[max(len(lines)-failedOutputLines, 0):] {
			r.status(iostream.LevelInfo, "    "+l)
		}
	case watchd.CheckErrored:
		r.status(iostream.LevelWarn, fmt.Sprintf("  %s could not run: %s", c.Name, c.Error))
	}
}

func msDuration(ms int64) time.Duration {
	return (time.Duration(ms) * time.Millisecond).Round(time.Second)
}

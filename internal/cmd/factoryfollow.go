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

// sessionPollInterval is how often `chunk factory build` asks the daemon how its
// run is going.
const sessionPollInterval = time.Second

// maxSessionPollFailures is how many polls in a row may fail before
// `chunk factory build` gives up following its run. The run is on the daemon and
// keeps going either way.
const maxSessionPollFailures = 5

// requireLocalDaemon refuses to run when the environment points at a remote
// daemon. A factory run works from files on this machine, so it belongs to the
// local daemon; a remote daemon would be working on a different checkout.
func requireLocalDaemon() error {
	if chunkd.CurrentConnection().Remote == "" {
		return nil
	}
	return newUserError("chunk factory build runs on the local chunk daemon, but CHUNK_DAEMON_REMOTE_ADDR is set.").
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
			e = e.withSuggestion("Follow the active one in 'chunk watch'.")
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

// reportLostContact says what following a factory run lost when it can no
// longer be followed: any setup lines it was holding back, said first so a
// failure during setup is never hidden, then where its work was last seen.
func reportLostContact(streams iostream.Streams, rep *sessionReporter, f *chunkd.FactoryRun) {
	rep.flushSetup()
	printLostFactoryWork(streams, f)
}

// collapseSetupNoise reports whether a factory run's setup progress should be
// held back behind a spinner rather than said line by line: only for a
// person watching a terminal, who gets nothing else to see it by. --verbose,
// --json and a non-terminal (chunk watch and --log share one, so this also
// leaves them alone) must still see every line.
func collapseSetupNoise(tty, jsonOut, verbose bool) bool {
	return tty && !jsonOut && !verbose
}

// followSession polls a factory session, reporting what changes, until it
// ends. Ctrl-C cancels it and follows it until it has wound down.
func followSession(ctx context.Context, streams iostream.Streams, id string, jsonOut, verbose bool) error {
	status := newStatusFunc(streams)
	live := newImplementerLine(streams.Err, status)
	defer live.clear()
	// Setup noise — the sidecars a run makes and syncs before its first round
	// — is only collapsed for a person watching a terminal; --verbose, --json
	// and the log (and a non-terminal, which chunk watch and --log share with)
	// must still see every line.
	_, tty := terminalFile(streams.Err)
	var setup *setupLine
	if collapseSetupNoise(tty, jsonOut, verbose) {
		setup = newSetupLine(streams.Err)
	}
	// Both progress lines sit under the status lines, so they come off the
	// screen before each one is written.
	clearLive := func() {
		live.clear()
		if setup != nil {
			setup.clear()
		}
	}
	defer clearLive()
	rep := newSessionReporter(func(level iostream.Level, msg string) {
		clearLive()
		status(level, msg)
	})
	if setup != nil {
		rep.collapseSetup(time.Now())
	}
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
			clearLive()
			reportLostContact(streams, rep, lastFactory)
			return sessionError(err)
		case err != nil:
			if failures++; failures >= maxSessionPollFailures {
				clearLive()
				reportLostContact(streams, rep, lastFactory)
				return newUserError("Lost contact with the chunk daemon.").
					withSuggestion(fmt.Sprintf("Run %s may still be running on the daemon. Follow it in 'chunk watch'.", id)).
					wrap(err)
			}
		default:
			failures = 0
			if detail.Factory != nil && detail.Factory.Worktree != "" {
				lastFactory = detail.Factory
			}
			rep.report(detail)
			live.update(detail)
			if setup != nil && rep.settingUp() {
				setup.update()
			}
			if detail.State.Finished() {
				clearLive()
				return finishFactory(streams, detail, jsonOut)
			}
		}
		select {
		case <-ctx.Done():
			clearLive()
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
	checks    map[string]bool
	// progress counts the lines of the factory run's progress already said.
	progress int
	// checked marks the factory rounds whose review findings have been said.
	checked map[int]bool

	// setup, once collapseSetup is called, holds a factory run's setup
	// progress — its sidecars being made and synced — instead of saying it,
	// until the first round appears or the session ends without one.
	setup *setupHold
}

// setupHold is the setup progress held back from a terminal's output while a
// factory run's sandboxes are not yet ready.
type setupHold struct {
	start time.Time
	held  []heldLine
	// open is false once the first round has appeared or the session has
	// ended without one; either way nothing more is held.
	open bool
}

// heldLine is one line of setup progress held for later, in case it must be
// said after all.
type heldLine struct {
	level iostream.Level
	msg   string
}

func newSessionReporter(status iostream.StatusFunc) *sessionReporter {
	return &sessionReporter{
		status:    status,
		rounds:    map[int]chunkd.RoundState{},
		reviews:   map[string]chunkd.PromptRunState{},
		implement: map[int]chunkd.RoundImplement{},
		checks:    map[string]bool{},
		checked:   map[int]bool{},
	}
}

// collapseSetup makes the reporter hold a factory run's pre-round setup
// progress instead of saying it: the sidecars it makes and syncs, which only
// matter if something goes wrong before any round. start is when following
// began, to measure how long setup took.
func (r *sessionReporter) collapseSetup(start time.Time) {
	r.setup = &setupHold{start: start, open: true}
}

// settingUp reports whether a factory run's first round has yet to appear,
// so a caller drawing a spinner in setup's place knows to keep redrawing it.
func (r *sessionReporter) settingUp() bool {
	return r.setup != nil && r.setup.open
}

// flushSetup says a factory run's held setup lines, as they would have been
// said, so a failure before any round is never hidden. It is a no-op once
// setup is no longer held.
func (r *sessionReporter) flushSetup() {
	if r.setup == nil || !r.setup.open {
		return
	}
	r.setup.open = false
	held := r.setup.held
	r.setup.held = nil
	for _, l := range held {
		r.status(l.level, l.msg)
	}
}

// sayProgress says a factory run's progress line, unless setup is being held
// back and the line is not the step line announcing it (which stays visible;
// it is not setup noise, it is the only sign setup has started at all).
func (r *sessionReporter) sayProgress(level iostream.Level, msg string) {
	if r.settingUp() && level != iostream.LevelStep {
		r.setup.held = append(r.setup.held, heldLine{level, msg})
		return
	}
	r.status(level, msg)
}

func (r *sessionReporter) report(d chunkd.SessionDetail) {
	s := d.Session
	// A factory run's progress comes first: most of it is the setup before
	// any round, which a snapshot caught up on all at once would otherwise
	// show after the rounds.
	if f := s.Factory; f != nil {
		for _, l := range f.Progress.Since(r.progress) {
			r.sayProgress(l.Level.Level(), l.Text)
		}
		r.progress = f.Progress.Total
	}
	if r.settingUp() {
		switch {
		case len(s.Rounds) > 0:
			// The sandboxes are ready: the held lines were only setup noise,
			// so they are dropped rather than said.
			r.setup.open = false
			r.setup.held = nil
			r.status(iostream.LevelDone, fmt.Sprintf("Sandboxes ready in %s", time.Since(r.setup.start).Round(time.Second)))
		case s.State.Finished():
			// The session ended before any round: setup itself must have
			// failed, so what it held is never hidden.
			r.flushSetup()
		}
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
		for _, c := range round.Checks {
			key := fmt.Sprintf("%d/%s", i, c.Name)
			if c.Status == chunkd.CheckQueued || c.Status == chunkd.CheckRunning || r.checks[key] {
				continue
			}
			r.checks[key] = true
			r.reportCheck(c)
		}
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

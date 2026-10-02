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

	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/gitremote"
	"github.com/CircleCI-Public/chunk-cli/internal/gitutil"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/watchd"
)

// sessionPollInterval is how often an attached `chunk session` asks the daemon
// how the session is going.
const sessionPollInterval = time.Second

// maxSessionPollFailures is how many polls in a row may fail before an attached
// command gives up. The session is on the daemon and keeps going either way.
const maxSessionPollFailures = 5

func newSessionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "session",
		Short: "Run and follow a pre-PR review session on the local watch daemon",
		Long: `A session reviews the work in the current project and applies the fixes,
in the background, on the local watch daemon. See 'chunk watch' to follow it
live. The session belongs to the daemon: attaching and detaching never changes
what it does, and only 'cancel' stops it.`,
		// Hidden until the rest of the flow (rebase, CI, approval, PR) exists.
		Hidden: true,
	}
	cmd.AddCommand(
		newSessionStartCmd(),
		newSessionAttachCmd(),
		newSessionCancelCmd(),
		newSessionResumeCmd(),
		newSessionRestoreCmd(),
		newSessionListCmd(),
	)
	return cmd
}

func newSessionStartCmd() *cobra.Command {
	var (
		projectDir, promptsDir, model string
		parallelism, rounds           int
		timeout                       time.Duration
		detach, jsonOut               bool
	)
	cmd := &cobra.Command{
		Use:          "start",
		Short:        "Start a session for the current project",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := requireLocalDaemon(); err != nil {
				return err
			}
			root, err := sessionProjectRoot(cmd.Context(), projectDir)
			if err != nil {
				return err
			}
			if err := watchd.EnsureRunning([]string{watchCmdName, watchDaemonSubcmd}); err != nil {
				return &userError{msg: "Could not start the watch daemon.", err: err}
			}
			id, err := watchd.StartSession(watchd.SessionRequest{
				ProjectRoot:    root,
				PromptsDir:     promptsDir,
				Parallelism:    parallelism,
				Model:          model,
				TimeoutSeconds: int(timeout / time.Second),
				MaxRounds:      rounds,
			})
			if err != nil {
				return sessionError(err)
			}
			streams := iostream.FromCmd(cmd)
			if detach {
				return printSessionID(streams, id, jsonOut)
			}
			streams.ErrPrintf("Session %s started on the watch daemon. Ctrl-C detaches; it keeps running.\n", id)
			return followSession(cmd.Context(), streams, id, jsonOut, false)
		},
	}
	cmd.Flags().StringVar(&projectDir, "project", "", "Project to review (default: the git repository containing the current directory)")
	cmd.Flags().StringVar(&promptsDir, "prompts", "", "Directory of review prompts, relative to the project (default: .chunk/reviews)")
	cmd.Flags().IntVar(&rounds, "rounds", 0, fmt.Sprintf("Review-and-fix rounds to run at most (default and maximum: %d)", watchd.MaxRounds))
	cmd.Flags().IntVar(&parallelism, "parallelism", 5, "Maximum sandboxes reviewing at once (0: one per prompt)")
	cmd.Flags().StringVar(&model, "model", "", "Claude model (default: Claude Code's default)")
	cmd.Flags().DurationVar(&timeout, "timeout", 0, "Max time for each Claude run (default: 15m)")
	cmd.Flags().BoolVar(&detach, "detach", false, "Print the session ID and return without following it")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

func newSessionAttachCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:          "attach <session-id>",
		Short:        "Follow a session until it ends or pauses",
		SilenceUsage: true,
		Args:         cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireLocalDaemon(); err != nil {
				return err
			}
			return followSession(cmd.Context(), iostream.FromCmd(cmd), args[0], jsonOut, false)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

func newSessionCancelCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "cancel <session-id>",
		Short:        "Stop a session (changes it already made stay; use restore to undo them)",
		SilenceUsage: true,
		Args:         cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireLocalDaemon(); err != nil {
				return err
			}
			if err := watchd.CancelSession(args[0]); err != nil {
				return sessionError(err)
			}
			iostream.FromCmd(cmd).ErrPrintf("Asked the daemon to cancel session %s.\n", args[0])
			return nil
		},
	}
}

func newSessionResumeCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "resume <session-id>",
		Short:        "Continue a paused session, taking your files as they are now",
		SilenceUsage: true,
		Args:         cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireLocalDaemon(); err != nil {
				return err
			}
			if err := watchd.ResumeSession(args[0]); err != nil {
				return sessionError(err)
			}
			iostream.FromCmd(cmd).ErrPrintf("Resumed session %s; it reviews your files as they are now.\n", args[0])
			return nil
		},
	}
}

func newSessionRestoreCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "restore <session-id>",
		Short: "Undo everything a session changed in your working tree",
		Long: `Puts back every file the session's fixes changed, as it was before the first
fix, and touches nothing else. Files you edited after the session left them are
not overwritten unless you pass --force. If the daemon has lost the session
(it restarted), the restore point is still in git: see docs/ARCHITECTURE.md.`,
		SilenceUsage: true,
		Args:         cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireLocalDaemon(); err != nil {
				return err
			}
			res, err := watchd.RestoreSession(args[0], force)
			if err != nil {
				return sessionError(err)
			}
			streams := iostream.FromCmd(cmd)
			for _, p := range res.Paths {
				streams.Printf("restored %s\n", p)
			}
			streams.ErrPrintf("Restored %d file(s).\n", len(res.Paths))
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "Also overwrite files you edited after the session changed them")
	return cmd
}

func newSessionListCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:          "list",
		Short:        "List the daemon's sessions",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := requireLocalDaemon(); err != nil {
				return err
			}
			sessions, err := watchd.ListSessions("")
			if err != nil {
				return sessionError(err)
			}
			streams := iostream.FromCmd(cmd)
			if jsonOut {
				return iostream.PrintJSON(streams.Out, sessions)
			}
			for _, s := range sessions {
				streams.Printf("%s  %-9s  %s  %d round(s)\n", s.ID, s.State, s.ProjectRoot, len(s.Rounds))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

// requireLocalDaemon refuses to run when the environment points at a remote
// daemon. A session works on files on this machine, so it belongs to the local
// daemon; a remote daemon would be reviewing a different checkout.
func requireLocalDaemon() error {
	if watchd.CurrentConnection().Remote == "" {
		return nil
	}
	return newUserError("Sessions run on the local watch daemon, but CHUNK_WATCHD_REMOTE_ADDR is set.").
		withCode("command.invalid_args").
		withSuggestion("Unset CHUNK_WATCHD_REMOTE_ADDR to use this command.").
		withExitCode(ExitBadArgs).
		withoutDetail()
}

// sessionProjectRoot names the project to review: the git repository containing
// projectDir (or the working directory), registered so the daemon can find it.
func sessionProjectRoot(ctx context.Context, projectDir string) (string, error) {
	dir := projectDir
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("determine working directory: %w", err)
		}
		dir = wd
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

func printSessionID(streams iostream.Streams, id string, jsonOut bool) error {
	if jsonOut {
		return iostream.PrintJSON(streams.Out, map[string]string{"id": id})
	}
	streams.Println(id)
	streams.ErrPrintf("Follow it with: chunk session attach %s   Stop it with: chunk session cancel %s\n", id, id)
	return nil
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
			e = e.withSuggestion("Follow the active one with 'chunk session attach <id>'.")
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
		f.Worktree, f.Branch, keepWorkHint(factory.Worktree{Path: f.Worktree, Branch: f.Branch, Baseline: f.Baseline, Head: f.Head}))
}

// followSession polls a session, reporting what changes, until it ends or
// pauses. Ctrl-C detaches and the session carries on, or with cancelOnInterrupt
// cancels it and follows it until it has wound down.
func followSession(ctx context.Context, streams iostream.Streams, id string, jsonOut, cancelOnInterrupt bool) error {
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
					withSuggestion(fmt.Sprintf("The session may still be running. Reattach with: chunk session attach %s", id)).
					wrap(err)
			}
		default:
			failures = 0
			if detail.IsFactory() && detail.Factory != nil && detail.Factory.Worktree != "" {
				lastFactory = detail.Factory
			}
			rep.report(detail)
			if detail.State.Finished() || detail.State == watchd.SessionPaused {
				if detail.IsFactory() {
					return finishFactory(ctx, streams, detail, jsonOut)
				}
				return finishSession(streams, detail, jsonOut)
			}
		}
		select {
		case <-ctx.Done():
			if !cancelOnInterrupt {
				streams.ErrPrintf("Detached. Session %s keeps running on the daemon (chunk session attach %s, or cancel %s).\n", id, id, id)
				return nil
			}
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

// finishSession prints where a session ended up and maps failure to an error.
func finishSession(streams iostream.Streams, detail watchd.SessionDetail, jsonOut bool) error {
	if jsonOut {
		return iostream.PrintJSON(streams.Out, detail)
	}
	switch detail.State {
	case watchd.SessionPaused:
		streams.Printf("Paused: %s\n", detail.PauseReason)
		streams.Printf("Continue with 'chunk session resume %s' (reviews your files as they are now), or 'chunk session cancel %s'.\n", detail.ID, detail.ID)
	case watchd.SessionCancelled:
		return newUserError("The session was cancelled.").withoutDetail()
	case watchd.SessionFailed:
		return &userError{msg: "The session failed: " + detail.Error, hideDetail: true, errMsg: "session failed"}
	case watchd.SessionRunning, watchd.SessionDone:
		// Summarised below.
	}
	for _, r := range detail.Rounds {
		streams.Printf("round %d: %d finding(s), %d worth changing%s%s\n", r.Number, r.Findings, r.Worth, fixSummary(r), roundNote(r))
	}
	if rp := detail.Restore; rp != nil && !rp.Restored {
		streams.Printf("Undo everything this session changed with: chunk session restore %s\n", detail.ID)
	}
	return nil
}

func fixSummary(r watchd.Round) string {
	if r.Fix == nil || r.Fix.State != watchd.FixApplied {
		return ""
	}
	return fmt.Sprintf(", fixed %d file(s) (+%d -%d)", len(r.Fix.Files), r.Fix.Insertions, r.Fix.Deletions)
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
	// activity and progress count the feed lines already said: each round's
	// implementer tools, and the factory run's own progress.
	activity map[int]int
	progress int
	// checked marks the factory rounds whose review findings have been said.
	checked map[int]bool
	state   watchd.SessionState
}

func newSessionReporter(status iostream.StatusFunc) *sessionReporter {
	return &sessionReporter{
		status:    status,
		rounds:    map[int]watchd.RoundState{},
		reviews:   map[string]watchd.PromptRunState{},
		implement: map[int]watchd.RoundImplement{},
		checks:    map[int]int{},
		activity:  map[int]int{},
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
			// A review session's notes are in its closing summary; a factory
			// run's say how each round went, which nothing else does.
			number, note := strconv.Itoa(round.Number), ""
			if s.IsFactory() {
				note = roundNote(round)
				if s.Factory != nil && s.Factory.Attempts > 0 {
					number += "/" + strconv.Itoa(s.Factory.Attempts)
				}
			}
			r.status(iostream.LevelStep, fmt.Sprintf("round %s: %s%s", number, round.State, note))
		}
		if impl := round.Implement; impl != nil {
			for _, l := range impl.Activity.Since(r.activity[i]) {
				r.status(l.Level.Level(), "  "+l.Text)
			}
			r.activity[i] = impl.Activity.Total
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
		if s.IsFactory() && round.State == watchd.RoundDone && !r.checked[i] {
			r.checked[i] = true
			r.reportFindings(roundResults(d, round.Number))
		}
	}
	if r.state != s.State && s.State == watchd.SessionPaused {
		r.status(iostream.LevelWarn, "paused: "+s.PauseReason)
	}
	r.state = s.State
}

// reportImplement says how a factory round's implementer turn went, once it
// has gone somewhere new.
func (r *sessionReporter) reportImplement(i int, impl watchd.RoundImplement) {
	prev := r.implement[i]
	r.implement[i] = impl
	if impl.State != prev.State {
		switch impl.State {
		case watchd.FixApplied:
			r.status(iostream.LevelDone, fmt.Sprintf("implementer finished in %s ($%.2f)", msDuration(impl.DurationMS), impl.CostUSD))
			if impl.Summary != "" {
				r.status(iostream.LevelInfo, oneLineSummary(impl.Summary))
			}
		case watchd.FixEmpty:
			r.status(iostream.LevelWarn, "no changes")
		case watchd.FixFailed:
			r.status(iostream.LevelWarn, "implementer failed: "+impl.Error)
		case watchd.FixRunning:
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
		switch factory.Status(res.Status) {
		case factory.StatusPassed:
			r.status(iostream.LevelDone, fmt.Sprintf("review %s: no findings worth changing", res.Prompt))
		case factory.StatusFailed:
			r.status(iostream.LevelError, fmt.Sprintf("review %s: %d finding(s)", res.Prompt, len(res.Findings)))
		case factory.StatusErrored:
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
	switch factory.Status(c.Status) {
	case factory.StatusPassed:
		r.status(iostream.LevelDone, fmt.Sprintf("  %s passed in %s", c.Name, msDuration(c.DurationMS)))
	case factory.StatusFailed:
		r.status(iostream.LevelError, fmt.Sprintf("  %s failed in %s", c.Name, msDuration(c.DurationMS)))
		if c.Output == "" {
			return
		}
		lines := strings.Split(c.Output, "\n")
		for _, l := range lines[max(len(lines)-failedOutputLines, 0):] {
			r.status(iostream.LevelInfo, "    "+l)
		}
	case factory.StatusErrored:
		r.status(iostream.LevelWarn, fmt.Sprintf("  %s could not run: %s", c.Name, c.Error))
	}
}

func msDuration(ms int64) time.Duration {
	return (time.Duration(ms) * time.Millisecond).Round(time.Second)
}

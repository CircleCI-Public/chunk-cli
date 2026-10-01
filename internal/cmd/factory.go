package cmd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/gitremote"
	"github.com/CircleCI-Public/chunk-cli/internal/gitutil"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/review"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/watchd"
)

// sessionPollInterval is how often an attached `chunk factory` asks the daemon
// how the session is going.
const sessionPollInterval = time.Second

// maxSessionPollFailures is how many polls in a row may fail before an attached
// command gives up. The session is on the daemon and keeps going either way.
const maxSessionPollFailures = 5

func newFactoryCmd() *cobra.Command {
	var (
		projectDir, reviewsDir, model, image string
		parallelism, rounds                  int
		implementTimeout, reviewTimeout      time.Duration
		noValidate, detach, jsonOut          bool
	)
	cmd := &cobra.Command{
		Use:   "factory <task>",
		Short: "Implement a task on a sidecar, then review, validate and fix it until it passes",
		Long: `Start a session on the local watch daemon that sends the task to an
implementer agent running on a sidecar, then loops: review its work with each
prompt in the reviews directory, each on a sidecar of its own, and run the
project's validation commands on the implementer's sidecar, and send what they
find back to the implementer, until every check passes or rounds run out.

The session works in a git worktree of its own, starting from your files as
they are, uncommitted changes included, and commits its work to a branch named
chunk/factory/<id>. Your checkout is never touched, so you can keep working.

The session belongs to the daemon: Ctrl-C detaches and it keeps running. Follow
it with 'chunk factory attach <id>' or 'chunk watch', and stop it with
'chunk factory cancel <id>'.`,
		// Hidden until the rest of the flow (rebase, CI, approval, PR) exists.
		Hidden:       true,
		SilenceUsage: true,
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) == 1 && strings.TrimSpace(args[0]) != "" {
				return nil
			}
			return newUserError("Pass the task as one argument.").
				withCode("command.invalid_args").
				withSuggestion(`Quote it: chunk factory "add a --verbose flag"`).
				withExitCode(ExitBadArgs).
				withoutDetail()
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if rounds < 0 || rounds > watchd.MaxRounds {
				return newUserError(fmt.Sprintf("--rounds must be between 1 and %d.", watchd.MaxRounds)).
					withCode("command.invalid_args").
					withExitCode(ExitBadArgs).
					withoutDetail()
			}
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
				ProjectRoot:             root,
				Task:                    args[0],
				PromptsDir:              reviewsDir,
				Parallelism:             parallelism,
				Model:                   model,
				Image:                   image,
				TimeoutSeconds:          int(reviewTimeout / time.Second),
				ImplementTimeoutSeconds: int(implementTimeout / time.Second),
				MaxRounds:               rounds,
				NoValidate:              noValidate,
			})
			if err != nil {
				return sessionError(err)
			}
			streams := iostream.FromCmd(cmd)
			if detach {
				return printSessionID(streams, id, jsonOut)
			}
			streams.ErrPrintf("Session %s started on the watch daemon. Ctrl-C detaches; it keeps running.\n", id)
			return followSession(cmd.Context(), streams, id, jsonOut)
		},
	}
	cmd.Flags().StringVar(&projectDir, "project", "", "Project to work on (default: the git repository containing the current directory)")
	cmd.Flags().StringVar(&reviewsDir, "reviews", "", fmt.Sprintf("Directory of review prompts, relative to the project (default: %s)", review.DefaultDir))
	cmd.Flags().IntVar(&rounds, "rounds", 0, fmt.Sprintf("Rounds of review, validation and fixes to run at most (default %d, maximum %d)", watchd.DefaultRounds, watchd.MaxRounds))
	cmd.Flags().IntVar(&parallelism, "parallelism", 5, "Maximum sidecars reviewing at once (0: one per prompt)")
	cmd.Flags().BoolVar(&noValidate, "no-validate", false, "Skip the project's validation commands")
	cmd.Flags().StringVar(&model, "model", "", "Claude model (default: Claude Code's default)")
	cmd.Flags().StringVar(&image, "image", "", "Snapshot image ID (default: validation.sidecarImage from config)")
	cmd.Flags().DurationVar(&implementTimeout, "implement-timeout", review.DefaultImplementTimeout, "Max time for each implementer turn")
	cmd.Flags().DurationVar(&reviewTimeout, "review-timeout", review.DefaultTimeout, "Max time for each review")
	cmd.Flags().BoolVar(&detach, "detach", false, "Print the session ID and return without following it")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	cmd.AddCommand(
		newFactoryAttachCmd(),
		newFactoryCancelCmd(),
		newFactoryListCmd(),
	)
	return cmd
}

func newFactoryAttachCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:          "attach <id>",
		Short:        "Follow a session until it ends",
		SilenceUsage: true,
		Args:         cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireLocalDaemon(); err != nil {
				return err
			}
			return followSession(cmd.Context(), iostream.FromCmd(cmd), args[0], jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

func newFactoryCancelCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "cancel <id>",
		Short:        "Stop a session; the work so far is still committed to its branch",
		SilenceUsage: true,
		Args:         cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireLocalDaemon(); err != nil {
				return err
			}
			if err := watchd.CancelSession(args[0]); err != nil {
				return sessionError(err)
			}
			iostream.FromCmd(cmd).ErrPrintf("Cancel requested for session %s.\n", args[0])
			return nil
		},
	}
}

func newFactoryListCmd() *cobra.Command {
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
				return iostream.PrintJSON(streams.Out, watchd.SessionList{Sessions: sessions})
			}
			if len(sessions) == 0 {
				streams.ErrPrintln(`No sessions. Start one with: chunk factory "<task>"`)
				return nil
			}
			for _, s := range sessions {
				state := string(s.State)
				if s.Outcome != "" {
					state += " (" + string(s.Outcome) + ")"
				}
				streams.Printf("%s  %-22s  %-34s  %s\n", s.ID, state, s.WorkBranch, firstLine(s.Task, 60))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

// requireLocalDaemon refuses to run when the environment points at a remote
// daemon. A session works on a checkout on this machine, so it belongs to the
// local daemon; a remote daemon would be working on a different checkout.
func requireLocalDaemon() error {
	if watchd.CurrentConnection().Remote == "" {
		return nil
	}
	return newUserError("Factory sessions run on the local watch daemon, but CHUNK_WATCHD_REMOTE_ADDR is set.").
		withCode("command.invalid_args").
		withSuggestion("Unset CHUNK_WATCHD_REMOTE_ADDR to use this command.").
		withExitCode(ExitBadArgs).
		withoutDetail()
}

// sessionProjectRoot names the project to work on: the git repository
// containing projectDir (or the working directory), registered so the daemon
// can find it.
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
	// The sidecar pool works out which repository to clone from the origin
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
	streams.ErrPrintf("Follow it with: chunk factory attach %s   Stop it with: chunk factory cancel %s\n", id, id)
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
		if refused.Status == http.StatusNotFound {
			e = e.withSuggestion("The daemon tracks projects it has seen; check the path or the session ID.")
		}
		return e
	}
	return fmt.Errorf("talk to the watch daemon: %w", err)
}

// followSession polls a session, reporting what changes, until it ends. Ctrl-C
// detaches; the session carries on.
func followSession(ctx context.Context, streams iostream.Streams, id string, jsonOut bool) error {
	rep := newSessionReporter(newStatusFunc(streams))
	failures := 0
	ticker := time.NewTicker(sessionPollInterval)
	defer ticker.Stop()
	for {
		detail, err := watchd.FetchSession(id)
		var refused *watchd.SessionRefused
		switch {
		case errors.As(err, &refused):
			return sessionError(err)
		case err != nil:
			if failures++; failures >= maxSessionPollFailures {
				return newUserError("Lost contact with the watch daemon.").
					withSuggestion(fmt.Sprintf("The session may still be running. Reattach with: chunk factory attach %s", id)).
					wrap(err)
			}
		default:
			failures = 0
			rep.report(detail.Session)
			if detail.State.Finished() {
				return finishSession(streams, detail, jsonOut)
			}
		}
		select {
		case <-ctx.Done():
			streams.ErrPrintf("Detached. Session %s keeps running on the daemon (chunk factory attach %s, or cancel %s).\n", id, id, id)
			return nil
		case <-ticker.C:
		}
	}
}

// finishSession prints where a session ended up and maps anything short of
// passing to an error.
func finishSession(streams iostream.Streams, detail watchd.SessionDetail, jsonOut bool) error {
	if jsonOut {
		if err := iostream.PrintJSON(streams.Out, detail); err != nil {
			return err
		}
		return sessionOutcomeError(detail.Session)
	}
	if fix := detail.Implement; fix != nil {
		streams.Printf("implement: %s\n", turnSummary(fix))
	}
	for _, r := range detail.Rounds {
		line := fmt.Sprintf("round %d: %d finding(s), %d worth changing", r.Number, r.Findings, r.Worth)
		if r.Fix != nil {
			line += "; fix: " + turnSummary(r.Fix)
		}
		if r.Note != "" {
			line += " — " + r.Note
		}
		streams.Println(line)
	}
	s := detail.Session
	switch {
	case s.WorkCommit != "" && s.WorkBranch != "":
		streams.Printf("The work is committed on %s. Take it with: git merge %s\n", s.WorkBranch, s.WorkBranch)
	case s.WorkDir != "":
		streams.Printf("The work could not be committed; it is still in %s.\n", s.WorkDir)
	}
	return sessionOutcomeError(s)
}

// sessionOutcomeError is the error a finished session returns, or nil when its
// checks all passed.
func sessionOutcomeError(s watchd.Session) error {
	switch s.State {
	case watchd.SessionCancelled:
		return newUserError("The session was cancelled.").withoutDetail()
	case watchd.SessionFailed:
		return &userError{msg: "The session failed: " + s.Error, hideDetail: true, errMsg: "session failed"}
	case watchd.SessionRunning, watchd.SessionDone:
	}
	switch s.Outcome {
	case watchd.OutcomePassed:
		return nil
	case watchd.OutcomeNoChange:
		return &userError{msg: "The implementer made no changes.", hideDetail: true, errMsg: "no changes"}
	case watchd.OutcomeStuck:
		return &userError{msg: "The implementer stopped changing the code with checks still failing.", hideDetail: true, errMsg: "stuck"}
	case watchd.OutcomeExhausted:
	}
	return &userError{msg: fmt.Sprintf("Checks still failed after %d round(s).", len(s.Rounds)), hideDetail: true, errMsg: "rounds exhausted"}
}

func turnSummary(f *watchd.RoundFix) string {
	switch f.State {
	case watchd.FixApplied:
		return fmt.Sprintf("changed %d file(s) (+%d -%d)", len(f.Files), f.Insertions, f.Deletions)
	case watchd.FixEmpty:
		return "no changes"
	case watchd.FixFailed:
		return "failed: " + f.Error
	case watchd.FixRunning:
		if f.Activity != "" {
			return firstLine(f.Activity, 120)
		}
	}
	return string(f.State)
}

// firstLine collapses the first line of s to at most n bytes.
func firstLine(s string, n int) string {
	line := strings.TrimSpace(strings.SplitN(strings.TrimSpace(s), "\n", 2)[0])
	if len(line) > n {
		return line[:n-3] + "..."
	}
	return line
}

// sessionReporter turns successive snapshots of a session into status lines,
// one each time something it follows changes. 'chunk watch' shows the rest.
type sessionReporter struct {
	status iostream.StatusFunc
	seen   map[string]string
}

func newSessionReporter(status iostream.StatusFunc) *sessionReporter {
	return &sessionReporter{status: status, seen: map[string]string{}}
}

func (r *sessionReporter) report(s watchd.Session) {
	if s.Implement != nil {
		r.say("implement", iostream.LevelInfo, "implementer: "+turnSummary(s.Implement))
	}
	for _, round := range s.Rounds {
		prefix := fmt.Sprintf("round %d", round.Number)
		r.say(prefix, iostream.LevelStep, fmt.Sprintf("%s: %s", prefix, round.State))
		for _, c := range round.Reviews {
			if c.State == watchd.PromptDone || c.State == watchd.PromptFailed {
				r.say(prefix+"/"+string(c.Kind)+"/"+c.Name, iostream.LevelInfo, checkLine(c))
			}
		}
		if round.Fix != nil {
			r.say(prefix+"/fix", iostream.LevelInfo, "implementer: "+turnSummary(round.Fix))
		}
	}
}

// say reports line under key unless it is what key last said.
func (r *sessionReporter) say(key string, level iostream.Level, line string) {
	if r.seen[key] == line {
		return
	}
	r.seen[key] = line
	r.status(level, line)
}

func checkLine(c watchd.ReviewPrompt) string {
	name := "  review " + c.Name
	if c.Kind == watchd.CheckValidate {
		name = "  $ " + c.Name
	}
	if c.State == watchd.PromptFailed {
		return name + " could not run: " + c.Error
	}
	return fmt.Sprintf("%s: %d finding(s)", name, c.Findings)
}

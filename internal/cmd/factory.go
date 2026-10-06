package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/review"
	"github.com/CircleCI-Public/chunk-cli/internal/watchd"
)

func newFactoryCmd() *cobra.Command {
	var attempts, reviewers int
	var keepSidecars, noValidate, jsonOut, logOn, verbose bool
	var orgID, image, model, reviewsDir, logFile, implementerInstructions string
	var implementTimeout, reviewTimeout time.Duration

	cmd := &cobra.Command{
		Use:   "factory [prompt|-]",
		Short: "Implement a prompt on a sidecar, then review and validate it until it passes",
		Long: `Send a prompt to an implementer agent running on a sidecar, then loop:
review its work with each prompt in the reviews directory, each on its own
sidecar, and run the project's validation commands, feeding failures back to
the implementer until every check passes or attempts run out.

The run happens on the local watch daemon, as a session: watch it in
'chunk watch'. Ctrl-C stops the run, and what the implementer did so far is
still committed.

The run works in a git worktree of its own, on the branch
chunk/factory/<run id>, starting from your files as they are, uncommitted
changes included. Your checkout is never touched. The implementer's work is
synced into the worktree each round and committed there when the run ends;
the worktree is kept so you can look at it or carry on in it.

With no prompt argument, the prompt is read from redirected stdin. A - prompt
selects stdin explicitly: chunk factory < prompt.md or chunk factory - < prompt.md.
A prompt argument with a file on stdin is refused rather than drop the file.

With --log, the run keeps a plain-text log in
~/.chunk/factory/run-<start time>.log, with its full context whatever the
display leaves out: every prompt the implementer is sent and what it did and
said, each review's findings in full, and each validation command's output
when it failed. --log-file FILE keeps the log in FILE instead, and on its own
turns the log on. --verbose adds the review prompts, the output of commands
that passed, and a check each round that every reviewer has the implementer's
change, and also shows the log here as the run writes it; it implies --log.`,
		SilenceUsage: true,
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) == 0 || len(args) == 1 && strings.TrimSpace(args[0]) != "" {
				return nil
			}
			return newUserError("Pass the prompt as one argument or on stdin.").
				withCode("command.invalid_args").
				withSuggestion(`Quote it: chunk factory "add a --verbose flag", or send a file on stdin: chunk factory < prompt.md`).
				withExitCode(ExitBadArgs).
				withoutDetail()
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			streams := iostream.FromCmd(cmd)
			prompt, err := factoryPrompt(cmd.InOrStdin(), args, logOn && logFile == "")
			if err != nil {
				return err
			}
			if attempts < 1 {
				return newUserError("--attempts must be at least 1.").
					withCode("command.invalid_args").
					withExitCode(ExitBadArgs).
					withoutDetail()
			}
			if err := requireLocalDaemon(); err != nil {
				return err
			}
			root, err := sessionProjectRoot(ctx, "")
			if err != nil {
				return err
			}
			cfg, err := config.LoadProjectConfig(root)
			if err != nil {
				return &userError{msg: msgValidateNotConfigured, suggestion: suggestionRunInit, err: err}
			}
			// The daemon loads these itself; loading them here first explains a
			// mistake before anything starts.
			if _, _, err := factoryChecks(root, reviewsDir, cfg, noValidate); err != nil {
				return err
			}
			relReviews, err := factoryReviewsDir(root, reviewsDir)
			if err != nil {
				return err
			}
			logArg, err := factoryLogPath(logFile, logOn || verbose, time.Now())
			if err != nil {
				return err
			}
			// The run appends to a log that is already there, so the tail
			// starts after whatever the file holds before the run is started.
			logFrom := logSize(logArg)

			if err := watchd.EnsureRunning([]string{watchCmdName, watchDaemonSubcmd}); err != nil {
				return &userError{msg: "Could not start the watch daemon.", err: err}
			}
			id, err := watchd.StartFactory(watchd.FactoryRequest{
				ProjectRoot:             root,
				Prompt:                  prompt,
				ReviewsDir:              relReviews,
				NoValidate:              noValidate,
				Attempts:                attempts,
				Reviewers:               reviewers,
				Model:                   model,
				ImplementerInstructions: implementerInstructions,
				ImplementTimeoutSeconds: int(implementTimeout / time.Second),
				ReviewTimeoutSeconds:    int(reviewTimeout / time.Second),
				KeepSidecars:            keepSidecars,
				OrgID:                   orgID,
				Image:                   image,
				Log:                     logArg,
				Verbose:                 verbose,
			})
			if err != nil {
				return sessionError(err)
			}
			streams.ErrPrintf("Factory run %s started on the watch daemon. Ctrl-C stops it.\n", id)
			if logArg != "" {
				streams.ErrPrintf("Logging to %s\n", logArg)
			}
			// With --verbose the log is shown here too, as the run writes it,
			// alongside the run's progress.
			if verbose && !jsonOut {
				defer tailLog(logArg, logFrom, streams.Err)()
			}
			return followSession(ctx, streams, id, jsonOut, true)
		},
	}

	cmd.Flags().IntVar(&attempts, "attempts", 3, "most rounds of review and validation")
	cmd.Flags().IntVar(&reviewers, "reviewers", 0, "reviewer sidecars (0: one per review prompt)")
	cmd.Flags().StringVar(&reviewsDir, "reviews", "", fmt.Sprintf("directory of review prompts (default: %s)", review.DefaultDir))
	cmd.Flags().BoolVar(&noValidate, "no-validate", false, "skip the project's validation commands")
	cmd.Flags().BoolVar(&keepSidecars, "keep-sidecars", false, "leave the sidecars running when the run ends")
	cmd.Flags().StringVar(&orgID, "org-id", "", "Organization ID")
	cmd.Flags().StringVar(&image, "image", "", "Snapshot image ID (default: validation.sidecarImage from config)")
	cmd.Flags().StringVar(&model, "model", "", "Claude model (default: Claude Code's default)")
	cmd.Flags().StringVar(&implementerInstructions, "implementer-instructions", "", "extra instructions for the implementer")
	cmd.Flags().DurationVar(&implementTimeout, "implement-timeout", factory.DefaultImplementTimeout, "max time for each implementer turn")
	cmd.Flags().DurationVar(&reviewTimeout, "review-timeout", review.DefaultTimeout, "max time for each review")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	cmd.Flags().BoolVar(&logOn, "log", false, "keep a log of the run's full context in ~/.chunk/factory/run-<start time>.log")
	cmd.Flags().StringVar(&logFile, "log-file", "", "keep the log in this file instead (implies --log)")
	cmd.Flags().BoolVar(&verbose, "verbose", false, "log more: review prompts, passing commands' output, reviewer checks; also show the log here (implies --log)")
	return cmd
}

// factoryPrompt is the prompt the implementer is sent: the sole argument when
// it is not -, and stdin otherwise. With no argument, terminal stdin is refused
// rather than waited on, since a missing redirect is the likelier mistake. An
// explicit - may read a terminal because the caller asked for stdin.
//
// An argument with a file redirected to stdin is refused too: the argument
// would win and the file's prompt be silently dropped. It is usually a word
// meant for a flag, as in --log run.log < prompt.md; bareLog says --log was
// given without --log-file, to point at that. Only a non-empty regular file
// counts, since a pipe or /dev/null is what scripts often inherit as stdin.
func factoryPrompt(in io.Reader, args []string, bareLog bool) (string, error) {
	if len(args) == 1 && args[0] != "-" {
		if redirectedFile(in) {
			return "", promptTwiceError(args[0], bareLog)
		}
		return args[0], nil
	}
	if len(args) == 0 {
		if f, ok := in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
			return "", missingFactoryPromptError()
		}
	}
	b, err := io.ReadAll(in)
	if err != nil {
		return "", &userError{msg: "Could not read the prompt from stdin.", err: err}
	}
	prompt := string(b)
	if strings.TrimSpace(prompt) == "" {
		return "", newUserError("The prompt on stdin is empty.").
			withCode("command.invalid_args").
			withExitCode(ExitBadArgs).
			withoutDetail()
	}
	return prompt, nil
}

// redirectedFile reports whether in is a regular file with something in it,
// as stdin is under < prompt.md.
func redirectedFile(in io.Reader) bool {
	f, ok := in.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	return err == nil && info.Mode().IsRegular() && info.Size() > 0
}

func promptTwiceError(arg string, bareLog bool) error {
	suggestion := `Pass the prompt as the argument or on stdin, not both.`
	if bareLog {
		suggestion = fmt.Sprintf("--log takes no file. To log to %s, write --log-file %s.", arg, arg)
	}
	return newUserError(fmt.Sprintf("Got the prompt %q as an argument and another prompt on stdin.", arg)).
		withCode("command.invalid_args").
		withSuggestion(suggestion).
		withExitCode(ExitBadArgs).
		withoutDetail()
}

func missingFactoryPromptError() error {
	return newUserError("Pass the prompt as an argument or on stdin.").
		withCode("command.invalid_args").
		withSuggestion(`chunk factory "add a --verbose flag", or chunk factory < prompt.md`).
		withExitCode(ExitBadArgs).
		withoutDetail()
}

// factoryChecks loads what the implementer's work is checked with: the review
// prompts and, unless noValidate, the validation commands. Having neither is an
// error, since the loop would then pass whatever the implementer wrote.
func factoryChecks(workDir, reviewsDir string, cfg *config.ProjectConfig, noValidate bool) ([]review.Prompt, []config.Command, error) {
	explicit := reviewsDir != ""
	if !explicit {
		reviewsDir = filepath.Join(workDir, review.DefaultDir)
	}
	prompts, commands, err := factory.LoadChecks(reviewsDir, !explicit, cfg, noValidate)
	switch {
	case err == nil:
		return prompts, commands, nil
	case errors.Is(err, factory.ErrNothingToCheck):
		return nil, nil, &userError{
			msg:        "Nothing to check the implementer's work with.",
			suggestion: fmt.Sprintf("Add review prompts to %s, or validation commands with 'chunk init'.", review.DefaultDir),
			hideDetail: true,
		}
	case errors.Is(err, review.ErrNoPrompts):
		return nil, nil, &userError{msg: fmt.Sprintf("No review prompts found in %s.", reviewsDir), suggestion: "Add one .md or .txt file per review.", err: err}
	}
	return nil, nil, &userError{msg: fmt.Sprintf("Could not read review prompts from %s.", reviewsDir), err: err}
}

// printFactoryWork summarizes the work committed on the run's branch and says
// how to keep it.
func printFactoryWork(ctx context.Context, wt factory.Worktree, status iostream.StatusFunc, streams iostream.Streams) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	stat, err := wt.Stat(ctx)
	if err != nil {
		status(iostream.LevelWarn, fmt.Sprintf("could not summarize the work on %s: %v", wt.Branch, err))
		streams.ErrPrintf("  Worktree: %s\n", wt.Path)
		return
	}
	if stat == "" {
		status(iostream.LevelInfo, "No changes to keep. The worktree is at "+wt.Path)
		return
	}
	status(iostream.LevelDone, fmt.Sprintf("Committed %s to %s", stat, wt.Branch))
	streams.ErrPrintf("  Worktree: %s\n  %s\n", wt.Path, keepWorkHint(wt))
}

// keepWorkHint says how to bring the run's work into the developer's checkout.
// A merge only works when the branch starts at their HEAD: when it starts from
// their uncommitted work committed as the baseline, a merge would collide with
// that same work still uncommitted in their checkout, so they apply the run's
// own changes on top of it instead.
func keepWorkHint(wt factory.Worktree) string {
	if wt.Baseline == wt.Head {
		return "Merge it with: git merge " + wt.Branch
	}
	return fmt.Sprintf("Apply it with: git diff --binary %s %s | git apply", wt.Baseline, wt.Branch)
}

// factoryReviewsDir is the --reviews directory relative to the project root,
// which is how the daemon is told it, or "" for the default.
func factoryReviewsDir(root, dir string) (string, error) {
	if dir == "" {
		return "", nil
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", dir, err)
	}
	// The root has its symlinks resolved, so the directory must too.
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", newUserError("--reviews must be a directory inside the project.").
			withCode("command.invalid_args").
			withExitCode(ExitBadArgs).
			withoutDetail()
	}
	return rel, nil
}

// tailPollInterval is how often tailLog looks for more of the log.
const tailPollInterval = 500 * time.Millisecond

// tailLog copies the log at path to w as the run writes it, from offset from,
// until the returned stop is called, which copies whatever is left and returns
// once it has. A log that already holds earlier runs, since the run appends to
// it, is followed from where it ended when the caller measured it, so they are
// not shown again.
func tailLog(path string, from int64, w io.Writer) (stop func()) {
	t := &logTail{w: w, path: path, off: from}
	done, finished := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(finished)
		ticker := time.NewTicker(tailPollInterval)
		defer ticker.Stop()
		for {
			t.follow()
			select {
			case <-done:
				t.follow()
				return
			case <-ticker.C:
			}
		}
	}()
	return func() {
		close(done)
		<-finished
	}
}

// logSize is how much the file at path holds, or 0 when there is no such file.
func logSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

// logTail copies the log at path to w as it grows, a whole line at a time.
type logTail struct {
	w    io.Writer
	path string
	off  int64
}

// follow copies whatever has been added to the log since the last call. A log
// not created yet, or unreadable for now, is tried again next time: the run
// writes it, and a hiccup reading it must not stop the follow.
func (t *logTail) follow() {
	f, err := os.Open(t.path)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Seek(t.off, io.SeekStart); err != nil {
		return
	}
	b, err := io.ReadAll(f)
	if err != nil {
		return
	}
	// A line still being written is left for the next call.
	end := bytes.LastIndexByte(b, '\n') + 1
	if end == 0 {
		return
	}
	_, _ = t.w.Write(plainText(b[:end]))
	t.off += int64(end)
}

// plainText drops the terminal control characters in b, other than newlines
// and tabs. The log holds what ran on the sidecars: command output and
// agents' text, which must not reach the terminal as commands of its own.
func plainText(b []byte) []byte {
	return bytes.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || !unicode.IsControl(r) {
			return r
		}
		return -1
	}, b)
}

// factoryLogPath is the log as the daemon is told it, or "" for no log: path
// when --log-file names one, else the default when on, as --log and --verbose
// turn it. It is absolute, since the daemon does not share this process's
// working directory, and settled here, so this command knows where the log is
// to show it. A default log is named for now, the run's start.
func factoryLogPath(path string, on bool, now time.Time) (string, error) {
	switch {
	case path == "" && !on:
		return "", nil
	case path == "":
		return factory.DefaultLogPath(now.UTC().Format("20060102-150405"))
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", path, err)
	}
	return abs, nil
}

// finishFactory prints where a factory run ended up and maps anything short of
// every check passing to an error.
func finishFactory(ctx context.Context, streams iostream.Streams, detail watchd.SessionDetail, jsonOut bool) error {
	status := newStatusFunc(streams)
	f := detail.Factory
	if jsonOut {
		if err := iostream.PrintJSON(streams.Out, detail); err != nil {
			return err
		}
		// The JSON is the report; the exit code still says whether it passed.
		status = func(iostream.Level, string) {}
	} else {
		printFactoryLeftovers(ctx, f, status, streams)
	}
	printFactoryTotals(detail, status)
	switch detail.State {
	case watchd.SessionCancelled:
		return newUserError("The factory run was cancelled.").withoutDetail()
	case watchd.SessionFailed:
		return &userError{msg: "The factory run failed: " + detail.Error, hideDetail: true, errMsg: "factory run failed"}
	case watchd.SessionRunning, watchd.SessionPaused, watchd.SessionDone:
	}
	return reportOutcome(status, factory.Outcome{Result: factory.Result(f.Result), Rounds: f.Rounds})
}

// printFactoryTotals says how long the run took and what the implementer's
// turns cost. What the reviews cost is not reported to chunk.
func printFactoryTotals(detail watchd.SessionDetail, status iostream.StatusFunc) {
	ended := time.Now()
	if detail.EndedAt != nil {
		ended = *detail.EndedAt
	}
	var cost float64
	turns := 0
	for _, r := range detail.Rounds {
		if impl := r.Implement; impl != nil && impl.State != watchd.FixRunning {
			cost += impl.CostUSD
			turns++
		}
	}
	status(iostream.LevelInfo, fmt.Sprintf("The run took %s; %d implementer turn(s) cost $%.2f.", ended.Sub(detail.StartedAt).Round(time.Second), turns, cost))
}

// printFactoryLeftovers says what a run left behind: its committed work, or
// where its uncommitted work still is. Sidecars kept running were said in the
// run's progress.
func printFactoryLeftovers(ctx context.Context, f *watchd.FactoryRun, status iostream.StatusFunc, streams iostream.Streams) {
	switch {
	case f.Committed:
		printFactoryWork(ctx, factory.Worktree{Path: f.Worktree, Branch: f.Branch, Baseline: f.Baseline, Head: f.Head}, status, streams)
	case f.Worktree != "":
		// A worktree removed after an early failure held no work.
		if _, err := os.Stat(f.Worktree); err == nil {
			status(iostream.LevelWarn, "The work was not committed. It is in the worktree "+f.Worktree)
		}
	}
}

// reportOutcome says why the loop stopped and returns an error unless every
// check passed. How each round's checks came out was said as it was checked.
func reportOutcome(status iostream.StatusFunc, o factory.Outcome) error {
	switch o.Result {
	case factory.ResultPassed:
		status(iostream.LevelDone, fmt.Sprintf("All checks passed after %d round(s).", o.Rounds))
		return nil
	case factory.ResultNoChange:
		return &userError{msg: "The implementer made no changes.", hideDetail: true, errMsg: "no changes"}
	case factory.ResultStuck:
		return &userError{msg: fmt.Sprintf("The implementer stopped changing the code after round %d, with checks still failing.", o.Rounds), hideDetail: true, errMsg: "stuck"}
	case factory.ResultExhausted:
	}
	return &userError{msg: fmt.Sprintf("Checks still failed after %d round(s).", o.Rounds), hideDetail: true, errMsg: "attempts exhausted"}
}

// oneLineSummary collapses text to one line short enough for a status line.
func oneLineSummary(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 160 {
		return s[:157] + "..."
	}
	return s
}

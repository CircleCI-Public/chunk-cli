package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/term"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/review"
)

func newFactoryCmd() *cobra.Command {
	// Build flags belong after the subcommand. Whitelisting unknown flags here
	// can consume the following prompt as a flag value and make a legacy
	// invocation print help with exit code 0 instead of reporting the mistake.
	cmd := &cobra.Command{
		Use:   "factory",
		Short: "Build and manage factory runs",
		RunE:  groupRunE,
	}
	cmd.AddCommand(newFactoryBuildCmd())
	return cmd
}

func newFactoryBuildCmd() *cobra.Command {
	var attempts, reviewers int
	var keepSidecars, noValidate, jsonOut, logOn, verbose bool
	var orgID, image, model, reviewsDir, logFile, implementerInstructions, continueRun string
	var implementTimeout, reviewTimeout time.Duration

	cmd := &cobra.Command{
		Use:   "build [prompt|-]",
		Short: "Implement a prompt on a sidecar, then review and validate it until it passes",
		Long: `Send a prompt to an implementer agent running on a sidecar, then loop:
review its work with each prompt in the reviews directory, each on its own
sidecar, and run the project's validation commands, feeding failures back to
the implementer until every check passes or attempts run out.

The run happens on the local chunk daemon, as a session: watch it in
'chunk watch'. Ctrl-C stops the run, and what the implementer did so far is
still committed.

The run works in a git worktree of its own, on the branch
chunk/factory/<run id>, starting from your files as they are, uncommitted
changes included. Your checkout is never touched. The implementer's work is
synced into the worktree each round and committed there when the run ends;
the worktree is kept so you can look at it or carry on in it.

With no prompt argument, the prompt is read from redirected stdin. A - prompt
selects stdin explicitly: chunk factory build < prompt.md or chunk factory build - < prompt.md.
A prompt argument with a file on stdin is refused rather than drop the file.

--continue RUN picks up the work a finished run left on its branch, such as
one whose checks still failed when its attempts ran out. RUN is the run's ID
or its branch, chunk/factory/<run id>. The run works in the same worktree and
adds a commit to the same branch, and its reviewers see the whole change. A
prompt is optional and adds to the original request: with one, the
implementer starts on it; without one, the work is checked first and the
implementer is sent what failed, in a round that does not count toward
--max-attempts.

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
				withSuggestion(`Quote it: chunk factory build "add a --verbose flag", or send a file on stdin: chunk factory build < prompt.md`).
				withExitCode(ExitBadArgs).
				withoutDetail()
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			streams := iostream.FromCmd(cmd)
			var prompt string
			var err error
			// A continued run needs no prompt, so stdin is only read for one when
			// asked for or redirected from a file.
			if continueRun == "" || len(args) > 0 || redirectedFile(cmd.InOrStdin()) {
				prompt, err = factoryPrompt(cmd.InOrStdin(), args, logOn && logFile == "")
				if err != nil {
					return err
				}
			}
			if cmd.Flags().Changed("log-file") && strings.TrimSpace(logFile) == "" {
				return newUserError("--log-file needs a file.").
					withCode("command.invalid_flags").
					withSuggestion("Write --log-file FILE, or --log to log to ~/.chunk/factory.").
					withExitCode(ExitBadArgs).
					withoutDetail()
			}
			if attempts < 1 {
				return newUserError("--max-attempts must be at least 1.").
					withCode("command.invalid_args").
					withExitCode(ExitBadArgs).
					withoutDetail()
			}
			if err := requireLocalDaemon(); err != nil {
				return err
			}
			root, err := sessionProjectRoot(ctx)
			if err != nil {
				return err
			}
			cfg, err := config.LoadProjectConfig(root)
			if err != nil {
				return &userError{msg: msgValidateNotConfigured, suggestion: suggestionRunInit, err: err, blocked: true}
			}
			// The daemon loads these itself; loading them here first explains a
			// mistake before anything starts.
			if _, _, err := factoryChecks(root, reviewsDir, cfg, noValidate); err != nil {
				return err
			}
			if continueRun != "" {
				if err := checkFactoryContinue(root, continueRun); err != nil {
					return err
				}
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

			if err := chunkd.EnsureRunning(); err != nil {
				return &userError{msg: "Could not start the chunk daemon.", err: err}
			}
			id, err := chunkd.StartFactory(chunkd.FactoryRequest{
				ProjectRoot:             root,
				Prompt:                  prompt,
				Continue:                continueRun,
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
			streams.ErrPrintf("Factory run %s started on the chunk daemon. Ctrl-C stops it.\n", id)
			if logArg != "" {
				streams.ErrPrintf("Logging to %s\n", logArg)
			}
			// With --verbose the log is shown here too, as the run writes it,
			// alongside the run's progress.
			if verbose && !jsonOut {
				defer tailLog(logArg, logFrom, streams.Err)()
			}
			return followSession(ctx, streams, id, jsonOut)
		},
	}

	cmd.Flags().IntVar(&attempts, "max-attempts", 3, "maximum rounds of review and validation")
	cmd.Flags().StringVar(&continueRun, "continue", "", "pick up the work of an earlier run, by its ID or branch")
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
	cmd.SetFlagErrorFunc(factoryFlagError)
	return cmd
}

// factoryFlagError points --log=FILE, which named the log's file before
// --log-file did, at --log-file instead of pflag's error about parsing a
// bool. Other flag errors go to the parent's handler.
func factoryFlagError(cmd *cobra.Command, err error) error {
	var invalid *pflag.InvalidValueError
	if errors.As(err, &invalid) && invalid.GetFlag().Name == "log" {
		file := invalid.GetValue()
		if file == "" {
			file = "FILE"
		}
		return newUserError("--log takes no file.").
			withCode("command.invalid_flags").
			withSuggestion(fmt.Sprintf("Write --log-file %s.", file)).
			withExitCode(ExitBadArgs).
			withoutDetail()
	}
	if cmd.HasParent() {
		return cmd.Parent().FlagErrorFunc()(cmd, err)
	}
	return err
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
		withSuggestion(`chunk factory build "add a --verbose flag", or chunk factory build < prompt.md`).
		withExitCode(ExitBadArgs).
		withoutDetail()
}

// checkFactoryContinue explains a run that cannot be continued before anything
// starts. The daemon loads the run's record itself.
func checkFactoryContinue(root, run string) error {
	_, err := factory.LoadRecord(root, factory.ParseRunID(run))
	switch {
	case err == nil:
		return nil
	case errors.Is(err, factory.ErrNoRecord):
		return newUserError(fmt.Sprintf("No factory run %s to continue in this project.", factory.ParseRunID(run))).
			withCode("command.invalid_args").
			withSuggestion("Pass the run ID from the end of the run's output, or its branch: chunk/factory/<run id>.").
			withExitCode(ExitBadArgs).
			withoutDetail()
	}
	return &userError{msg: fmt.Sprintf("Could not read factory run %s.", factory.ParseRunID(run)), err: err}
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
			blocked:    true,
		}
	case errors.Is(err, review.ErrNoPrompts):
		return nil, nil, &userError{msg: fmt.Sprintf("No review prompts found in %s.", reviewsDir), suggestion: "Add one .md or .txt file per review.", err: err, blocked: true}
	}
	return nil, nil, &userError{msg: fmt.Sprintf("Could not read review prompts from %s.", reviewsDir), err: err}
}

// printFactoryWork summarizes the work committed on the run's branch and says
// how to keep it. The daemon worked out the summary as the run ended.
func printFactoryWork(f *chunkd.FactoryRun, status iostream.StatusFunc, streams iostream.Streams) {
	if f.StatError != "" {
		status(iostream.LevelWarn, fmt.Sprintf("could not summarize the work on %s: %s", f.Branch, f.StatError))
		streams.ErrPrintf("  Worktree: %s\n", f.Worktree)
		return
	}
	if f.Stat == "" {
		status(iostream.LevelInfo, "No changes to keep. The worktree is at "+f.Worktree)
		return
	}
	status(iostream.LevelDone, fmt.Sprintf("Committed %s to %s", f.Stat, f.Branch))
	streams.ErrPrintf("  Worktree: %s\n  %s\n", f.Worktree, keepWorkHint(f))
}

// keepWorkHint says how to bring the run's work into the developer's checkout.
// A merge only works when the branch starts at their HEAD: when it starts from
// their uncommitted work committed as the baseline, a merge would collide with
// that same work still uncommitted in their checkout, so they apply the run's
// own changes on top of it instead.
func keepWorkHint(f *chunkd.FactoryRun) string {
	if f.Baseline == f.Head {
		return "Merge it with: git merge " + f.Branch
	}
	return fmt.Sprintf("Apply it with: git diff --binary %s %s | git apply", f.Baseline, f.Branch)
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
func finishFactory(streams iostream.Streams, detail chunkd.SessionDetail, jsonOut bool) error {
	status := newStatusFunc(streams)
	f := detail.Factory
	if jsonOut {
		if err := iostream.PrintJSON(streams.Out, detail); err != nil {
			return err
		}
		// The JSON is the report; the exit code still says whether it passed.
		status = func(iostream.Level, string) {}
	} else {
		printFactoryLeftovers(f, status, streams)
		printFactoryContinueHint(f, status)
	}
	printFactoryTotals(detail, status)
	switch detail.State {
	case chunkd.SessionCancelled:
		return newUserError("The factory run was cancelled.").withoutDetail()
	case chunkd.SessionFailed:
		return &userError{msg: "The factory run failed: " + detail.Error, hideDetail: true, errMsg: "factory run failed"}
	case chunkd.SessionRunning, chunkd.SessionDone:
	}
	return reportOutcome(status, f.Result, f.Rounds)
}

// printFactoryTotals says how long the run took and what the implementer's
// turns cost. What the reviews cost is not reported to chunk.
func printFactoryTotals(detail chunkd.SessionDetail, status iostream.StatusFunc) {
	ended := time.Now()
	if detail.EndedAt != nil {
		ended = *detail.EndedAt
	}
	var cost float64
	turns := 0
	for _, r := range detail.Rounds {
		if impl := r.Implement; impl != nil && impl.State != chunkd.ImplementRunning {
			cost += impl.CostUSD
			turns++
		}
	}
	status(iostream.LevelInfo, fmt.Sprintf("The run took %s; %d implementer turn(s) cost $%.2f.", ended.Sub(detail.StartedAt).Round(time.Second), turns, cost))
}

// printFactoryLeftovers says what a run left behind: its committed work, or
// where its uncommitted work still is. Sidecars kept running were said in the
// run's progress.
func printFactoryLeftovers(f *chunkd.FactoryRun, status iostream.StatusFunc, streams iostream.Streams) {
	switch {
	case f.Committed:
		printFactoryWork(f, status, streams)
	case f.Worktree != "" && !f.WorktreeRemoved:
		// A worktree removed after an early failure held no work.
		status(iostream.LevelWarn, "The work was not committed. It is in the worktree "+f.Worktree)
	}
}

// printFactoryContinueHint says how to carry on a run whose committed work
// still fails its checks. The branch names the run whose record a continued
// run reads, the first in a chain of them.
func printFactoryContinueHint(f *chunkd.FactoryRun, status iostream.StatusFunc) {
	failing := f.Result == string(factory.ResultExhausted) || f.Result == string(factory.ResultStuck)
	if !failing || !f.Committed || f.Branch == "" {
		return
	}
	status(iostream.LevelInfo, fmt.Sprintf(`Keep working on it with: chunk factory build --continue %s ["what to do differently"]`, factory.ParseRunID(f.Branch)))
}

// reportOutcome says why the loop stopped and returns an error unless every
// check passed. How each round's checks came out was said as it was checked.
func reportOutcome(status iostream.StatusFunc, result string, rounds int) error {
	switch result {
	case chunkd.ResultPassed:
		status(iostream.LevelDone, fmt.Sprintf("All checks passed after %d round(s).", rounds))
		return nil
	case chunkd.ResultNoChange:
		return &userError{msg: "The implementer made no changes.", hideDetail: true, errMsg: "no changes"}
	case chunkd.ResultStuck:
		return &userError{msg: fmt.Sprintf("The implementer stopped changing the code after round %d, with checks still failing.", rounds), hideDetail: true, errMsg: "stuck"}
	case chunkd.ResultExhausted:
	}
	return &userError{msg: fmt.Sprintf("Checks still failed after %d round(s).", rounds), hideDetail: true, errMsg: "attempts exhausted"}
}

// oneLineSummary collapses text to one line short enough for a status line.
func oneLineSummary(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 160 {
		return s[:157] + "..."
	}
	return s
}

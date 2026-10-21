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
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
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
	cmd.AddCommand(newFactoryPullCmd())
	cmd.AddCommand(newFactoryStartCmd())
	cmd.AddCommand(newFactoryPlanCmd())
	cmd.AddCommand(newFactoryBootstrapCmd())
	cmd.AddCommand(newFactoryEvalCmd())
	return cmd
}

// prFlags are the three flags that decide what a run does with its branch once
// every check has passed.
type prFlags struct {
	// open is --open-pr, which insists on a pull request, and none is
	// --no-open-pr, which wants none. With neither, a run opens one if the
	// project can.
	open, none, draft bool
}

// mode is what the daemon is asked for.
func (f prFlags) mode() string {
	switch {
	case f.none:
		return chunkd.PullRequestNone
	case f.open:
		return chunkd.PullRequestRequired
	}
	return chunkd.PullRequestAuto
}

// check rejects the combinations that contradict each other.
func (f prFlags) check() error {
	switch {
	case f.open && f.none:
		return newUserError("--open-pr and --no-open-pr ask for opposite things.").
			withCode("command.invalid_flags").
			withSuggestion("A run opens a pull request when its checks pass; leave both out for that, or keep only --no-open-pr.").
			withExitCode(ExitBadArgs).
			withoutDetail()
	case f.draft && f.none:
		return newUserError("--draft has nothing to open as a draft with --no-open-pr.").
			withCode("command.invalid_flags").
			withSuggestion("Drop --no-open-pr to open the pull request as a draft.").
			withExitCode(ExitBadArgs).
			withoutDetail()
	}
	return nil
}

// checkFactoryBuildFlags rejects flag combinations that cannot run, before the
// daemon or the project is touched.
func checkFactoryBuildFlags(cmd *cobra.Command, logFile string, pr prFlags, remote bool, attempts int) error {
	switch {
	// A remote run does not go through the local daemon, which is what opens the
	// pull request, so asking for one would be quietly ignored.
	case remote && (pr.open || pr.draft):
		return newUserError("--open-pr and --draft do not work with --remote yet.").
			withCode("command.invalid_flags").
			withSuggestion("Drop them to run remotely, or drop --remote to open a pull request when the checks pass.").
			withExitCode(ExitBadArgs).
			withoutDetail()
	case cmd.Flags().Changed("log-file") && strings.TrimSpace(logFile) == "":
		return newUserError("--log-file needs a file.").
			withCode("command.invalid_flags").
			withSuggestion("Write --log-file FILE, or --log for the default log.").
			withExitCode(ExitBadArgs).
			withoutDetail()
	case attempts < 1:
		return newUserError("--max-attempts must be at least 1.").
			withCode("command.invalid_args").
			withExitCode(ExitBadArgs).
			withoutDetail()
	}
	return pr.check()
}

func newFactoryBuildCmd() *cobra.Command {
	var attempts, reviewers int
	var keepSidecars, noValidate, jsonOut, logOn, verbose, remote bool
	var pr prFlags
	var orgID, image, model, reviewsDir, logFile, implementerInstructions, continueRun, chunkBinary string
	var implementTimeout, reviewTimeout time.Duration

	cmd := &cobra.Command{
		Use:   "build [prompt|file|-]",
		Short: "Implement a prompt on a sidecar, then review and validate it until it passes",
		Long: `Send a prompt to an implementer agent running on a sidecar, then loop:
review its work with each prompt in the reviews directory, each on its own
sidecar, and run the project's validation commands, feeding failures back to
the implementer until every check passes or attempts run out.

The run happens on the local chunk daemon, as a session: watch it in
'chunk watch'. Ctrl-C stops the run, and what the implementer did so far is
still committed.

The run works in a git worktree of its own, on a branch named from the prompt:
chunk/factory/<description>/<run id>. It starts from your files as they are,
uncommitted changes included. Your checkout is never touched. The implementer's
work is synced into the worktree each round and committed there when the run
ends; the worktree is kept so you can look at it or carry on in it.

A single argument with no spaces that names an existing file is read as the
prompt, wherever the file is: chunk factory build ../notes/spec.md reads that
file and still runs on this repo, never the file's own. The run's record
keeps the file's path as where the prompt came from, shown in 'chunk watch'
too. A missing .md, .markdown or .txt path is an error rather than literal
text, since that is a far likelier mistyped path than a prompt; a directory, a
binary file and a file over 1 MiB are refused the same way. Anything else,
such as a quoted sentence, is the prompt itself, even one that names a file:
chunk factory build "fix README.md typos".

With no prompt argument, the prompt is read from redirected stdin. A - prompt
selects stdin explicitly: chunk factory build < prompt.md or chunk factory build - < prompt.md.
A prompt argument with a file on stdin is refused rather than drop the file.

--continue RUN picks up the work a finished run left on its branch, such as
one whose checks still failed when its attempts ran out. RUN is the run's ID
or its branch, chunk/factory/<description>/<run id>. The run works in the same
worktree and adds a commit to the same branch, and its reviewers see the whole
change. A prompt is optional and adds to the original request: with one, the
implementer starts on it; without one, the work is checked first and the
implementer is sent what failed, in a round that counts toward
--max-attempts. Without a prompt in a terminal, it asks for one first, and
Enter skips it.

A round is one implementer pass, then the reviews and the project's checks.
The run stops when every check passes or after --max-attempts rounds (3 by
default), and says what is left. Raise it when the change is large or the
reviewers are strict and runs keep ending with feedback left, so a run can
finish without --continue. Lower it, down to 1, for quick or exploratory
changes or to cap spend: each round costs an implementer turn plus every
reviewer, so more rounds cost more and take longer.

Once every check has passed the run pushes its branch to origin and opens a
pull request on GitHub, ready for review (--draft opens a draft). That needs a
GitHub token ('chunk auth set github' or GITHUB_TOKEN, then restart the daemon)
with write access to pull requests, and an origin on GitHub; without them the
run still runs and says why it opened none. The pull request targets the branch
you started the run from if origin has it, and otherwise the repository's
default branch. A run whose checks did not pass pushes nothing and opens
nothing. --no-open-pr leaves the branch alone, and --open-pr insists on the
pull request: a project that could not open one is refused before the run
starts, rather than after the minutes it takes.

With --log, the run keeps a plain-text log in
~/.local/share/chunk/repo-<id>/factory/<start time>.log, beside the run's
record, with its full context whatever the display leaves out: every prompt
the implementer is sent and what it did and said, each review's findings in
full, and each validation command's output when it failed. --log-file FILE keeps the log in FILE instead, and on its own
turns the log on. --verbose adds the review prompts, the output of commands
that passed, and a check each round that every reviewer has the implementer's
change, and also shows the log here as the run writes it; it implies --log.

--remote provisions a sidecar of its own for the run instead of using the
local daemon, and execs this same command on it, so you can close your laptop
or lose your connection without stopping the run. Its stderr streams to this
terminal as it goes; Ctrl-C detaches rather than stopping it, and
'chunk factory pull' brings the work back later, whether you detached or let
it run to completion. --continue, --log, --log-file and --verbose are not
supported with it yet, and it always leaves the branch alone (--open-pr and
--draft are refused): a remote run does not go through the local daemon,
which is what pushes and opens the pull request.`,
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
			var prompt, specOrigin string
			var err error
			if factoryBuildNeedsPrompt(continueRun, args, cmd.InOrStdin()) {
				prompt, specOrigin, err = factoryPrompt("chunk factory build", cmd.InOrStdin(), args, logOn && logFile == "")
				if err != nil {
					return err
				}
			}
			if err := checkFactoryBuildFlags(cmd, logFile, pr, remote, attempts); err != nil {
				return err
			}
			if chunkBinary != "" && !remote {
				return newUserError("--chunk-binary needs --remote.").
					withCode("command.invalid_flags").
					withExitCode(ExitBadArgs).
					withoutDetail()
			}
			// A remote run works on a sidecar of its own; the local daemon plays
			// no part in it, so it branches off before anything that needs one.
			if remote {
				return runFactoryBuildRemote(cmd, factoryRemoteOptions{
					prompt:                  prompt,
					orgID:                   orgID,
					image:                   image,
					chunkBinary:             chunkBinary,
					reviewsDir:              reviewsDir,
					continueRun:             continueRun,
					logOn:                   logOn,
					logFile:                 logFile,
					verbose:                 verbose,
					jsonOut:                 jsonOut,
					reviewers:               reviewers,
					noValidate:              noValidate,
					keepSidecars:            keepSidecars,
					model:                   model,
					implementerInstructions: implementerInstructions,
					implementTimeout:        implementTimeout,
					reviewTimeout:           reviewTimeout,
					attempts:                attempts,
				})
			}
			if err := requireLocalDaemon(); err != nil {
				return err
			}
			root, err := sessionProjectRoot(ctx)
			if err != nil {
				return err
			}
			cfg, err := loadFactoryConfig(root)
			if err != nil {
				return err
			}
			// The daemon loads these itself; loading them here first explains a
			// mistake before anything starts.
			if _, _, err := factoryChecks(root, reviewsDir, cfg, noValidate); err != nil {
				return err
			}
			// Not an error: a project whose commands need nothing installed
			// runs fine on the default image. But one that does fails every
			// round on a missing toolchain, so say why up front.
			if image == "" && !cfg.HasSidecarImage() {
				streams.ErrPrintln(ui.Dim("No validation.sidecarImage, so sidecars start from the default image. 'chunk factory bootstrap' sets one up."))
			}
			if continueRun != "" {
				if err := checkFactoryContinue(root, continueRun); err != nil {
					return err
				}
				if prompt == "" {
					if prompt, err = askContinueGuidance(cmd.InOrStdin(), jsonOut); err != nil {
						return err
					}
				}
			}
			relReviews, err := factoryReviewsDir(root, reviewsDir)
			if err != nil {
				return err
			}
			logArg, err := factoryLogPath(root, logFile, logOn || verbose, factory.NewRunID())
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
				SpecOrigin:              specOrigin,
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
				PullRequest:             pr.mode(),
				Draft:                   pr.draft,
			})
			if err != nil {
				return sessionError(err)
			}
			printRunStartMessage(streams, jsonOut, runStartMessage(pr, false, continueRun != "", attempts))
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

	cmd.Flags().IntVar(&attempts, "max-attempts", 3, maxAttemptsUsage)
	cmd.Flags().StringVar(&continueRun, "continue", "", "pick up an earlier run by its ID (the branch's final segment) or full branch")
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
	cmd.Flags().BoolVar(&logOn, "log", false, "keep a log of the run's full context in ~/.local/share/chunk/repo-<id>/factory/<start time>.log")
	cmd.Flags().StringVar(&logFile, "log-file", "", "keep the log in this file instead (implies --log)")
	cmd.Flags().BoolVar(&verbose, "verbose", false, "log more: review prompts, passing commands' output, reviewer checks; also show the log here (implies --log)")
	cmd.Flags().BoolVar(&pr.open, "open-pr", false, "refuse to start the run unless its pull request could be opened")
	cmd.Flags().BoolVar(&pr.none, "no-open-pr", false, "do not push the branch or open a pull request when every check passes")
	cmd.Flags().BoolVar(&pr.draft, "draft", false, "open the pull request as a draft")
	cmd.Flags().BoolVar(&remote, "remote", false, "provision a sidecar of its own for this run, so you can close your laptop; bring the work back with 'chunk factory pull'")
	cmd.Flags().StringVar(&chunkBinary, "chunk-binary", "", "Linux chunk build to upload to the remote sidecar with --remote (default: install the release matching this build)")
	_ = cmd.Flags().MarkHidden("chunk-binary")
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

// factoryPrompt is the prompt the implementer is sent, and where it came
// from: the sole argument when it is not -, and stdin otherwise. With no
// argument, terminal stdin is refused rather than waited on, since a missing
// redirect is the likelier mistake. An explicit - may read a terminal because
// the caller asked for stdin. use is the command's own invocation ("chunk
// factory build" or "chunk factory start"), named in error suggestions.
//
// An argument with a file redirected to stdin is refused too: the argument
// would win and the file's prompt be silently dropped. It is usually a word
// meant for a flag, as in --log run.log < prompt.md; bareLog says --log was
// given without --log-file, to point at that. Only a non-empty regular file
// counts, since a pipe or /dev/null is what scripts often inherit as stdin.
func factoryPrompt(use string, in io.Reader, args []string, bareLog bool) (prompt, specOrigin string, err error) {
	if len(args) == 1 && args[0] != "-" {
		// Checked before the argument is resolved: a stray word meant for a
		// flag, as in --log run.log < prompt.md, should get the specific
		// "got a prompt twice" message, not be judged as a possible file path
		// in its own right just because it happens to end in .md or .txt.
		if redirectedFile(in) {
			return "", "", promptTwiceError(args[0], bareLog)
		}
		var argErr error
		prompt, specOrigin, argErr = promptFromArg(use, args[0])
		if argErr != nil {
			return "", "", argErr
		}
		return prompt, specOrigin, nil
	}
	if len(args) == 0 {
		if f, ok := in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
			return "", "", missingFactoryPromptError()
		}
	}
	b, err := io.ReadAll(in)
	if err != nil {
		return "", "", &userError{msg: "Could not read the prompt from stdin.", err: err}
	}
	prompt = string(b)
	if strings.TrimSpace(prompt) == "" {
		return "", "", newUserError("The prompt on stdin is empty.").
			withCode("command.invalid_args").
			withExitCode(ExitBadArgs).
			withoutDetail()
	}
	return prompt, "", nil
}

// maxPromptFileSize caps how much of a file build and start will read as a
// prompt: comfortably more than anyone writes by hand, so a bigger file is
// more likely the wrong path than a very long prompt.
const maxPromptFileSize = 1 << 20 // 1 MiB

// promptFileExtensions are the extensions that make a missing word an error
// rather than literal text: typing the wrong path to a spec is a more likely
// mistake than wanting one of these literally in a prompt.
var promptFileExtensions = []string{".md", ".markdown", ".txt"}

// promptFromArg resolves build and start's sole prompt argument. A word with
// no whitespace that names an existing regular file is read as the prompt,
// wherever the file is - inside the project, a sibling repository or
// anywhere else - and specOrigin is its path; the run still works on the
// project where the command ran, and the file's own repository, if it has
// one, is never touched or synced. A word with no whitespace and a
// promptFileExtensions extension that does not exist is an error, never a
// prompt: that is a far likelier mistyped path than literal text. Anything
// else - an argument with whitespace, or a single word that is not an
// existing file - is literal prompt text, as always.
func promptFromArg(use, arg string) (prompt, specOrigin string, err error) {
	if hasWhitespace(arg) {
		return arg, "", nil
	}
	info, statErr := os.Stat(arg)
	switch {
	case statErr == nil && info.IsDir():
		return "", "", promptPathError(use, fmt.Sprintf("%s is a directory.", arg))
	case statErr == nil && info.Mode().IsRegular():
		filePrompt, fileErr := readPromptFile(use, arg, info)
		return filePrompt, arg, fileErr
	case statErr == nil:
		return "", "", promptPathError(use, fmt.Sprintf("%s is not a regular file.", arg))
	case os.IsNotExist(statErr) && hasPromptFileExtension(arg):
		return "", "", promptPathError(use, fmt.Sprintf("%s: file not found.", arg))
	}
	return arg, "", nil
}

// hasWhitespace reports whether s contains a whitespace rune, which rules out
// its being a single word, let alone a plausible file path.
func hasWhitespace(s string) bool {
	return strings.IndexFunc(s, unicode.IsSpace) >= 0
}

// hasPromptFileExtension reports whether path's extension names it as a
// prompt file, case-insensitively.
func hasPromptFileExtension(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	for _, e := range promptFileExtensions {
		if ext == e {
			return true
		}
	}
	return false
}

// readPromptFile reads path, already known to be a regular file, as a
// prompt. info is its os.Stat, reused to check the size cap without a second
// stat.
func readPromptFile(use, path string, info os.FileInfo) (string, error) {
	if info.Size() > maxPromptFileSize {
		return "", promptPathError(use, fmt.Sprintf("%s is over %s, too large to read as a prompt.", path, formatByteSize(maxPromptFileSize)))
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", &userError{msg: fmt.Sprintf("Could not read %s.", path), err: err}
	}
	if isBinaryPromptFile(b) {
		return "", promptPathError(use, fmt.Sprintf("%s looks like a binary file.", path))
	}
	prompt := string(b)
	if strings.TrimSpace(prompt) == "" {
		return "", promptPathError(use, fmt.Sprintf("%s is empty.", path))
	}
	return prompt, nil
}

// isBinaryPromptFile reports whether b looks like a binary file: a NUL byte
// in its first chunk, the heuristic git and most editors use, since a text
// file never contains one.
func isBinaryPromptFile(b []byte) bool {
	return bytes.IndexByte(b[:min(len(b), 8000)], 0) >= 0
}

// formatByteSize renders n for a person, in whole mebibytes since that is the
// only size chunk ever caps a prompt file at.
func formatByteSize(n int64) string {
	return fmt.Sprintf("%d MiB", n/(1<<20))
}

// promptPathError reports that arg could not be read as a prompt file, for
// the reason in msg, before anything else about the run starts. The
// suggestion offers stdin, the only escape hatch there is: quoting a single
// word reaches the command as the same argument, so it cannot be what tells
// a path from literal text.
func promptPathError(use, msg string) error {
	return newUserError(msg).
		withCode("command.invalid_args").
		withSuggestion(fmt.Sprintf(`Fix the path, or send the prompt on stdin: %s < file.md, or pipe the text to %s -`, use, use)).
		withExitCode(ExitBadArgs).
		withoutDetail()
}

// factoryBuildNeedsPrompt reports whether the build command should read a
// prompt from the argument or stdin. A continued run needs no prompt, so
// stdin is only read for one when asked for or redirected from a file.
func factoryBuildNeedsPrompt(continueRun string, args []string, in io.Reader) bool {
	return continueRun == "" || len(args) > 0 || redirectedFile(in)
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

// promptContinueGuidance asks what a continued run should do differently. It
// returns ui.ErrNoTTY without asking when there is no terminal to ask on, or
// when --json says a program is reading the output, so a script's continue
// checks first as it always has. Swapped out in tests.
var promptContinueGuidance = func(in io.Reader, jsonOut bool) (string, error) {
	f, ok := in.(*os.File)
	if nonInteractive() || jsonOut || !ok || !term.IsTerminal(int(f.Fd())) || ui.RequireStdoutTTY() != nil {
		return "", ui.ErrNoTTY
	}
	return ui.PromptText("What should it do differently? (Enter to just re-check)", "")
}

// askContinueGuidance is the prompt for a continued run given none: what the
// developer types, or "" to check the work first.
func askContinueGuidance(in io.Reader, jsonOut bool) (string, error) {
	guidance, err := promptContinueGuidance(in, jsonOut)
	switch {
	case errors.Is(err, ui.ErrNoTTY):
		return "", nil
	case errors.Is(err, ui.ErrCancelled):
		return "", &userError{msg: "No factory run started.", err: err, hideDetail: true}
	case err != nil:
		return "", fmt.Errorf("ask what to do differently: %w", err)
	}
	return strings.TrimSpace(guidance), nil
}

// checkFactoryContinue explains a run that cannot be continued before anything
// starts. The daemon loads the run's record itself.
func checkFactoryContinue(root, run string) error {
	rec, err := factory.LoadRecord(root, factory.ParseRunID(run))
	switch {
	case err == nil:
		if rec.Remote != nil {
			return newUserError(fmt.Sprintf("Factory run %s is on a sidecar; its work is not local yet.", factory.ParseRunID(run))).
				withCode("command.invalid_args").
				withExitCode(ExitBadArgs).
				withoutDetail()
		}
		return nil
	case errors.Is(err, factory.ErrNoRecord):
		return newUserError(fmt.Sprintf("No factory run %s to continue in this project.", factory.ParseRunID(run))).
			withCode("command.invalid_args").
			withSuggestion("Pass the run ID from the end of the run's output, or its branch: chunk/factory/<description>/<run id>.").
			withExitCode(ExitBadArgs).
			withoutDetail()
	}
	return &userError{msg: fmt.Sprintf("Could not read factory run %s.", factory.ParseRunID(run)), err: err}
}

// loadFactoryConfig loads the project's config, pointing a project that has
// none at bootstrap. One that is there but cannot be read is reported as such:
// bootstrap would refuse it too, so it is not the way out.
func loadFactoryConfig(root string) (*config.ProjectConfig, error) {
	cfg, err := loadBootstrapConfig(root)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return nil, &userError{msg: msgValidateNotConfigured, suggestion: "Run 'chunk factory bootstrap' to set the project up for chunk factory, or 'chunk init' for chunk validate alone.", err: os.ErrNotExist, hideDetail: true, blocked: true}
	}
	return cfg, nil
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
			suggestion: fmt.Sprintf("Run 'chunk factory bootstrap' to write review prompts to %s, or add validation commands with 'chunk init'.", review.DefaultDir),
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
// to show it. A default log is named name, in the repository at root; a name
// made by factory.NewRunID keeps runs started in the same second apart.
func factoryLogPath(root, path string, on bool, name string) (string, error) {
	switch {
	case path == "" && !on:
		return "", nil
	case path == "":
		return factory.DefaultLogPath(root, name)
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
		printFactoryPullRequest(f, status)
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
	if err := reportOutcome(status, f); err != nil {
		return err
	}
	return reportPullRequest(f)
}

// printFactoryPullRequest says what became of the run's pull request, where
// the progress already shown has not. The run's progress said it when one was
// opened or refused, as it happened, so only the rest is said here.
func printFactoryPullRequest(f *chunkd.FactoryRun, status iostream.StatusFunc) {
	pr := f.PullRequest
	if pr == nil {
		return
	}
	switch pr.State {
	case chunkd.PRSkipped:
		status(iostream.LevelInfo, "No pull request opened: "+pr.Message)
	case chunkd.PRPending, chunkd.PRPushing, chunkd.PROpening:
		status(iostream.LevelWarn, "The pull request was not finished: the run ended while it was "+pr.State)
	case chunkd.PROpened, chunkd.PRFailed:
	}
}

// reportPullRequest is an error when the checks passed but the pull request
// could not be opened: opening one is part of what the command does, so a
// script waiting on it should not be told the run worked. A project with
// nothing to open one with skips it instead, and is no error. The work is safe
// on the branch either way.
func reportPullRequest(f *chunkd.FactoryRun) error {
	if f.PullRequest == nil || f.PullRequest.State != chunkd.PRFailed {
		return nil
	}
	return &userError{
		msg:        "All checks passed, but the pull request could not be opened: " + f.PullRequest.Message,
		suggestion: fmt.Sprintf("The work is committed on %s. Push it and open the pull request yourself, or fix the cause and run again.", f.Branch),
		hideDetail: true,
		errMsg:     "pull request failed",
	}
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
// has not passed its checks, including one that failed or was cancelled part
// way. The branch names the run whose record a continued
// run reads, the first in a chain of them. A run that ran out of rounds says
// it in its error instead, beside how to raise the cap.
func printFactoryContinueHint(f *chunkd.FactoryRun, status iostream.StatusFunc) {
	if !f.Continuable() || f.Result == chunkd.ResultExhausted {
		return
	}
	status(iostream.LevelInfo, fmt.Sprintf(`Keep working on it with: %s ["what to do differently"]`, f.ContinueCommand()))
}

// maxAttemptsUsage is --max-attempts' help. Cobra adds "(default 3)".
const maxAttemptsUsage = "maximum rounds of implement, review and check. Raise for large changes; lower to cap cost"

// reportOutcome says why the loop stopped and returns an error unless every
// check passed. How each round's checks came out was said as it was checked.
func reportOutcome(status iostream.StatusFunc, f *chunkd.FactoryRun) error {
	result, rounds := f.Result, f.Rounds
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
	return exhaustedError(f)
}

// exhaustedError says a run used every round it was allowed with checks still
// failing, and how to go further: a higher --max-attempts next time, or
// --continue to carry this run's work on now, when it committed any.
func exhaustedError(f *chunkd.FactoryRun) *userError {
	rounds := fmt.Sprintf("%d rounds", f.Rounds)
	if f.Rounds == 1 {
		rounds = "1 round"
	}
	suggestion := "Raise the cap with --max-attempts <n>."
	if f.Continuable() {
		suggestion = fmt.Sprintf(`Raise the cap with --max-attempts <n>, or carry on with: %s ["what to do differently"]`, f.ContinueCommand())
	}
	return &userError{
		msg:        fmt.Sprintf("Checks still failed after %s: the run reached its cap (--max-attempts).", rounds),
		suggestion: suggestion,
		hideDetail: true,
		errMsg:     "attempts exhausted",
	}
}

// oneLineSummary collapses text to one line short enough for a status line.
func oneLineSummary(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 160 {
		return string(r[:157]) + "..."
	}
	return s
}

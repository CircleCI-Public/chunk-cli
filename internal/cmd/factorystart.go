package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/CircleCI-Public/chunk-cli/internal/claudecode"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/factorystart"
)

func newFactoryStartCmd() *cobra.Command {
	var jsonOut bool
	var diffFile string

	cmd := &cobra.Command{
		Use:   "start [prompt|-]",
		Short: "Hand the factory a diff and a prompt to refine into a mergeable change",
		Long: `Hand the factory a diff and a prompt: the prompt says what the change is
for, and the diff is a first pass at it. The factory refines the diff against
the prompt into a mergeable change. Where the diff comes from is up to you:

Make it in the run's worktree, with Claude Code, another agent or by hand:

  cd "$(chunk factory start "add a --verbose flag")"
  # ...work on it...
  chunk factory build --continue <run id>

Or pass a patch made somewhere else with --diff, such as git diff's output.
It is applied to your files in the worktree as the first pass. A - reads it
from stdin, with the prompt as an argument:

  git -C ../scratch diff | chunk factory start --diff - "add a --verbose flag"

Either way, chunk factory build --continue <run id> hands the diff and the
prompt to the factory: its implementer is told the prompt and finishes the
diff, and its reviewers check the work against the prompt. The diff is
whatever changed in the worktree, committed or not.

The worktree starts from your files as they are, uncommitted changes included,
on a branch named chunk/factory/<description>/<run id>, like a build's.
Nothing runs on a sidecar until you continue. The worktree's path is printed on
stdout, and how to continue on stderr. A - prompt, or none with stdin
redirected, reads the prompt from stdin.

Run with no prompt in a terminal to have Claude Code make the first pass:

  chunk factory start

You type the prompt, Claude Code works on it in the worktree on your machine,
with your own login, and you see the diff it made. Enter hands the diff and the
prompt to the factory, as chunk factory build --continue would; q keeps it in
the worktree for later.`,
		SilenceUsage: true,
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) == 0 || len(args) == 1 && strings.TrimSpace(args[0]) != "" {
				return nil
			}
			return newUserError("Pass the prompt as one argument or on stdin.").
				withCode("command.invalid_args").
				withSuggestion(`Quote it: chunk factory start "add a --verbose flag"`).
				withExitCode(ExitBadArgs).
				withoutDetail()
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			streams := iostream.FromCmd(cmd)
			// Stdin is the diff, so the prompt can only be an argument, and a -
			// prompt would read the diff's stdin a second time.
			if diffFile == "-" && (len(args) == 0 || args[0] == "-") {
				return newUserError("With --diff -, pass the prompt as an argument.").
					withCode("command.invalid_args").
					withSuggestion(`git -C ../scratch diff | chunk factory start --diff - "add a --verbose flag"`).
					withExitCode(ExitBadArgs).
					withoutDetail()
			}
			if f, ok := cmd.InOrStdin().(*os.File); ok && len(args) == 0 && term.IsTerminal(int(f.Fd())) {
				// The TUI needs the terminal on stdout too, so cd "$(chunk
				// factory start)" is still refused rather than open it.
				if diffFile == "" && !jsonOut && !nonInteractive() && ui.RequireStdoutTTY() == nil {
					return runFactoryStartTUI(cmd)
				}
				return newUserError("Pass the prompt as an argument or on stdin.").
					withCode("command.invalid_args").
					withSuggestion(`chunk factory start "add a --verbose flag", or chunk factory start < prompt.md`).
					withExitCode(ExitBadArgs).
					withoutDetail()
			}
			var diff []byte
			if diffFile != "" {
				var err error
				if diff, err = readDiff(cmd.InOrStdin(), diffFile); err != nil {
					return err
				}
			}
			var prompt string
			var err error
			if diffFile == "-" {
				prompt = args[0]
			} else if prompt, err = factoryPrompt(cmd.InOrStdin(), args, false); err != nil {
				return err
			}
			// The run that refines the work needs what a build does, origin
			// included, so a project without it is refused before anything
			// is made.
			root, err := sessionProjectRoot(ctx)
			if err != nil {
				return err
			}
			rec, err := factory.Start(ctx, root, factory.Handoff{Prompt: prompt, Diff: diff})
			if errors.Is(err, factory.ErrDiffAlreadyApplied) {
				return newUserError("The diff is already in your files, so the worktree starts with it.").
					withCode("command.invalid_args").
					withSuggestion(`Leave out --diff: chunk factory start "add a --verbose flag"`).
					withExitCode(ExitBadArgs).
					withoutDetail()
			}
			if err != nil {
				return &userError{msg: "Could not start a factory run.", err: err}
			}
			if jsonOut {
				return iostream.PrintJSON(streams.Out, rec)
			}
			status := newStatusFunc(streams)
			status(iostream.LevelDone, fmt.Sprintf("Started factory run %s on %s", rec.RunID, rec.Branch))
			streams.ErrPrintf("  Work on the change in the worktree, then refine it with: chunk factory build --continue %s\n", rec.RunID)
			streams.Printf("%s\n", rec.Worktree)
			return nil
		},
	}

	cmd.Flags().StringVar(&diffFile, "diff", "", "start from this patch of work done elsewhere (- for stdin)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

// runFactoryStartTUI has Claude Code make the first pass at a prompt the
// developer types, shows them the diff, and hands it to the factory or keeps
// it in the run's worktree, as they choose.
func runFactoryStartTUI(cmd *cobra.Command) error {
	ctx := cmd.Context()
	streams := iostream.FromCmd(cmd)
	root, err := sessionProjectRoot(ctx)
	if err != nil {
		return err
	}
	if err := factory.CheckClaude(); err != nil {
		return newUserError("Claude Code is not installed, so it cannot make the first pass.").
			withCode("command.missing_dependency").
			withSuggestion(`Install it from https://claude.com/claude-code, or make the first pass yourself: chunk factory start "add a --verbose flag"`).
			withExitCode(ExitBadArgs).
			wrap(err)
	}
	out, err := factorystart.Run(ctx, firstPassWork(root))
	if err != nil {
		return &userError{msg: "Could not show the prompt.", err: err}
	}
	p := out.Pass
	status := newStatusFunc(streams)
	switch {
	case out.Choice == factorystart.Cancelled:
		return &userError{msg: "No factory run started.", err: ui.ErrCancelled, hideDetail: true}
	case out.Choice == factorystart.HandOff:
		status(iostream.LevelDone, fmt.Sprintf("Handing factory run %s to the factory", p.RunID))
		return handOffFirstPass(cmd, p.RunID)
	case p.RunID == "":
		return &userError{msg: "Could not start a factory run.", err: out.Err}
	case errors.Is(out.Err, context.Canceled):
		status(iostream.LevelWarn, fmt.Sprintf("Stopped Claude. What it did so far is in factory run %s.", p.RunID))
	case out.Err != nil:
		status(iostream.LevelError, fmt.Sprintf("Claude's first pass in factory run %s failed.", p.RunID))
	default:
		status(iostream.LevelDone, fmt.Sprintf("Kept factory run %s", p.RunID))
	}
	streams.ErrPrintf("  Work on the change in the worktree, then refine it with: chunk factory build --continue %s\n", p.RunID)
	streams.Printf("%s\n", p.Worktree)
	if out.Err != nil && !errors.Is(out.Err, context.Canceled) {
		return &userError{msg: "Claude could not make the first pass.", err: out.Err}
	}
	return nil
}

// firstPassWork starts a run for the prompt and has Claude Code make the
// first pass in its worktree.
func firstPassWork(root string) factorystart.Work {
	return func(ctx context.Context, prompt string, activity func(string)) (factorystart.Pass, error) {
		rec, err := factory.Start(ctx, root, factory.Handoff{Prompt: prompt})
		if err != nil {
			return factorystart.Pass{}, err
		}
		pass := factorystart.Pass{RunID: rec.RunID, Worktree: rec.Worktree}
		turn, err := factory.FirstPass(ctx, rec, func(a claudecode.Activity) { activity(activityLine(a)) })
		pass.Summary = turn.Summary
		if err != nil {
			return pass, err
		}
		pass.Stat, err = factory.WorkStat(ctx, rec)
		return pass, err
	}
}

// handOffFirstPass refines a first pass as 'chunk factory build --continue'
// does. Stdin is left out, so it is not asked what to do differently: the
// pass was just made for the prompt.
func handOffFirstPass(cmd *cobra.Command, runID string) error {
	build := newFactoryBuildCmd()
	build.SetArgs([]string{"--continue", runID})
	build.SetIn(strings.NewReader(""))
	build.SetOut(cmd.OutOrStdout())
	build.SetErr(cmd.ErrOrStderr())
	build.SilenceErrors = true
	return build.ExecuteContext(cmd.Context())
}

// readDiff reads the patch --diff names, from stdin for -. An empty one is
// refused: there would be nothing to refine.
func readDiff(stdin io.Reader, name string) ([]byte, error) {
	var b []byte
	var err error
	if name == "-" {
		b, err = io.ReadAll(stdin)
	} else {
		b, err = os.ReadFile(name)
	}
	if err != nil {
		return nil, &userError{msg: "Could not read the diff.", err: err}
	}
	if strings.TrimSpace(string(b)) == "" {
		return nil, newUserError("The diff is empty, so there is nothing to refine.").
			withCode("command.invalid_args").
			withSuggestion("Leave out --diff to start the work in the worktree instead.").
			withExitCode(ExitBadArgs).
			withoutDetail()
	}
	return b, nil
}

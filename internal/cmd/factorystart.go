package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
)

func newFactoryStartCmd() *cobra.Command {
	var jsonOut bool
	var diffFile string

	cmd := &cobra.Command{
		Use:   "start [prompt|file|-]",
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

A single argument with no spaces that names an existing file is read as the
prompt, wherever the file is, as for 'chunk factory build': the run still
starts on this repo, never the file's own, and the run's record keeps the
file's path. A missing .md, .markdown or .txt path is an error rather than
literal text; a directory, a binary file and a file over 1 MiB are refused
the same way.`,
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
			var prompt, specOrigin string
			var err error
			if diffFile == "-" {
				prompt, specOrigin, err = promptFromArg("chunk factory start", args[0])
				if err != nil {
					return err
				}
			} else if prompt, specOrigin, err = factoryPrompt("chunk factory start", cmd.InOrStdin(), args, false); err != nil {
				return err
			}
			// The run that refines the work needs what a build does, origin
			// included, so a project without it is refused before anything
			// is made.
			root, err := sessionProjectRoot(ctx)
			if err != nil {
				return err
			}
			rec, err := factory.Start(ctx, root, factory.Handoff{Prompt: prompt, SpecOrigin: specOrigin, Diff: diff})
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

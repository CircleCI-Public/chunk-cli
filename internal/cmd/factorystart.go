package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
)

func newFactoryStartCmd() *cobra.Command {
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "start [prompt|-]",
		Short: "Make a worktree to start a change in by hand, for a factory run to refine",
		Long: `Make a factory run's worktree and branch for you to start a change in
yourself, with Claude Code, another agent or none, then hand what you did to
the factory to refine into a mergeable change:

  cd "$(chunk factory start "add a --verbose flag")"
  # ...work on it...
  chunk factory build --continue <run id>

The worktree starts from your files as they are, uncommitted changes included,
on a branch named chunk/factory/<description>/<run id>, like a build's.
Nothing runs on a sidecar yet. The prompt says what the change is for: the
continued run's implementer is told it, and its reviewers check the work
against it. Whatever you do in the worktree is the change they see, committed
or not.

The worktree's path is printed on stdout, and how to continue on stderr. A -
prompt, or none with stdin redirected, reads the prompt from stdin.`,
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
			if f, ok := cmd.InOrStdin().(*os.File); ok && len(args) == 0 && term.IsTerminal(int(f.Fd())) {
				return newUserError("Pass the prompt as an argument or on stdin.").
					withCode("command.invalid_args").
					withSuggestion(`chunk factory start "add a --verbose flag", or chunk factory start < prompt.md`).
					withExitCode(ExitBadArgs).
					withoutDetail()
			}
			prompt, err := factoryPrompt(cmd.InOrStdin(), args, false)
			if err != nil {
				return err
			}
			// The run that refines the work needs what a build does, origin
			// included, so a project without it is refused before anything
			// is made.
			root, err := sessionProjectRoot(ctx)
			if err != nil {
				return err
			}
			rec, err := factory.Start(ctx, root, prompt)
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

	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

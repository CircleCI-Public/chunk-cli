package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/claudecode"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/plan"
)

func newFactoryPlanCmd() *cobra.Command {
	var model string

	cmd := &cobra.Command{
		Use:   "plan [prompt]",
		Short: "Plan a change with Claude, then send the plan to the factory",
		Long: `Plan a change in a chat with Claude Code on this machine. The plan Claude
writes is shown beside the chat as it changes; give feedback until it is right,
then press ctrl+s to send it to the factory to build, as
'chunk factory build <plan file>' would.`,
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			streams := iostream.FromCmd(cmd)
			if err := ui.RequireStdoutTTY(); err != nil {
				return fmt.Errorf("factory plan requires a TTY")
			}
			if err := requireLocalDaemon(); err != nil {
				return err
			}
			if err := factory.CheckClaude(); err != nil {
				return newUserError("Claude Code is not installed on this machine.").
					withSuggestion("Install it from https://claude.com/claude-code.").
					withoutDetail()
			}
			root, err := sessionProjectRoot(ctx)
			if err != nil {
				return err
			}
			if err := checkFactoryProject(ctx, root); err != nil {
				return err
			}
			planPath, err := newPlanPath(root)
			if err != nil {
				return err
			}

			turn, submit := planSession(root, planPath, model)

			var first string
			if len(args) == 1 {
				first = args[0]
			}
			// Claude runs as a process of its own; this context stops it if
			// the screen is closed mid-turn.
			planCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			screen := plan.New(planCtx, turn, submit, first).
				WithTerminal(lipgloss.HasDarkBackground(os.Stdin, os.Stdout), false)
			p := tea.NewProgram(plan.Screen{Model: screen}, tea.WithContext(ctx))
			final, err := p.Run()
			if err != nil {
				return err
			}
			m := final.(plan.Screen).Model
			if m.RunID == "" {
				if _, err := os.Stat(planPath); err == nil {
					streams.ErrPrintf("The plan was not sent. It is saved at %s; build it later with: chunk factory build %s\n", planPath, planPath)
				}
				return nil
			}
			streams.ErrPrintf("Factory run %s started on the chunk daemon from %s.\n", m.RunID, planPath)
			streams.ErrPrintln("Follow along with `chunk watch`, or leave it running.")
			return nil
		},
	}

	cmd.Flags().StringVar(&model, "model", "", "model Claude plans with (default: your Claude Code default)")
	return cmd
}

// newPlanPath is where a new plan for root is kept: in the project's data
// directory, out of the working tree, so a plan is never committed by mistake.
func newPlanPath(root string) (string, error) {
	dataDir, err := config.ProjectDataDir(root)
	if err != nil {
		return "", fmt.Errorf("data dir for %s: %w", root, err)
	}
	dir := filepath.Join(dataDir, "plans")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create the plans directory: %w", err)
	}
	return filepath.Join(dir, time.Now().Format("20060102-150405")+".md"), nil
}

// planSession is a conversation with Claude about the plan at planPath, in
// root, for the planning screen: its turns, and the send of the plan it ends
// with to the factory.
func planSession(root, planPath, model string) (plan.Turner, plan.Submitter) {
	session := &factory.PlanSession{Dir: root, PlanPath: planPath, Model: model}
	turn := func(ctx context.Context, message string, activity func(string)) (string, string, error) {
		reply, err := session.Turn(ctx, message, func(a claudecode.Activity) { activity(activityLine(a)) })
		p, planErr := session.Plan()
		return reply, p, errors.Join(err, planErr)
	}
	submit := func(text string) (string, error) {
		if err := chunkd.EnsureRunning(); err != nil {
			return "", fmt.Errorf("start the chunk daemon: %w", err)
		}
		return sendPlan(root, planPath, text)
	}
	return turn, submit
}

// sendPlan hands an approved plan to the factory, as 'chunk factory build
// <planPath>' would.
func sendPlan(root, planPath, text string) (string, error) {
	id, err := chunkd.StartFactory(chunkd.FactoryRequest{
		ProjectRoot: root,
		Prompt:      strings.TrimSpace(text),
		SpecOrigin:  planPath,
		PullRequest: chunkd.PullRequestAuto,
	})
	var refused *chunkd.SessionRefused
	switch {
	case errors.As(err, &refused):
		return "", errors.New(refused.Message)
	case errors.Is(err, chunkd.ErrDaemonUnavailable):
		return "", errors.New("the chunk daemon is not reachable")
	case err != nil:
		return "", fmt.Errorf("talk to the chunk daemon: %w", err)
	}
	return id, nil
}

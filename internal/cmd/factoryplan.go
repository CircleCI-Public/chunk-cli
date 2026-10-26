package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/authprompt"
	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/factoryplan"
	"github.com/CircleCI-Public/chunk-cli/internal/gitutil"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/plan"
)

func newFactoryPlanCmd() *cobra.Command {
	var model string

	cmd := &cobra.Command{
		Use:   "plan [prompt]",
		Short: "Plan a change with Claude, then send the plan to the factory",
		Long: `Plan a change in a chat with a factory planning session of Claude Code on
this machine. The plan is kept in factory and shown beside the chat as it
changes; comment on it until it is right, then press ctrl+s to send it to the
factory to build.

Needs factory, its f plugin for Claude Code, and the factory daemon running
with FACTORY_PLAN_STORE=circleci as the CircleCI user chunk is signed in as.`,
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stopHangup := untilHangup(cmd.Context())
			defer stopHangup()
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
			if err := factory.CheckFactoryDaemon(ctx); err != nil {
				return agentStartError(err)
			}
			root, err := sessionProjectRoot(ctx)
			if err != nil {
				return err
			}
			if err := checkFactoryProject(ctx, root); err != nil {
				return err
			}
			api, err := factoryPlanClient()
			if err != nil {
				return err
			}

			// The session runs as a process of its own, in a process group of
			// its own, so it would outlive chunk: it is stopped, and waited
			// for, as the screen closes.
			planCtx, cancel := context.WithCancel(ctx)
			agent, submit, store := planSession(planCtx, root, model, api)
			// Cancelling first ends a session that is still starting, which
			// Stop would otherwise wait out.
			defer func() {
				cancel()
				_ = agent.Stop()
			}()

			var first string
			if len(args) == 1 {
				first = args[0]
			}
			// The branch is only for the header; a repo without one has none.
			branch, _ := gitutil.CurrentBranchIn(root)
			screen := plan.New(planCtx, agent, submit, store, first).
				WithTerminal(lipgloss.HasDarkBackground(os.Stdin, os.Stdout), false).
				WithBranch(branch)
			p := tea.NewProgram(plan.Screen{Model: screen}, tea.WithContext(ctx))
			final, err := p.Run()
			if err != nil {
				return err
			}
			m := final.(plan.Screen).Model
			if m.RunID == "" {
				if id := store.ID(); id != "" {
					streams.ErrPrintf("The plan was not sent. It is plan %s in factory.\n", id)
				}
				return nil
			}
			streams.ErrPrintf("Factory run %s started on the chunk daemon from plan %s in factory.\n", m.RunID, store.ID())
			if showHints(streams, false) {
				streams.ErrPrintln(watchStartHint)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&model, "model", "", "model Claude plans with (default: your Claude Code default)")
	return cmd
}

// factoryPlanClient resolves the CircleCI client the planning screen's store
// talks to, from the developer's saved token. It never prompts: the
// planning screen is not the place to start an interactive login, so a
// missing token comes back as a plain error naming how to fix it.
func factoryPlanClient() (*circleci.Client, error) {
	rc, err := config.ResolveCircleCI(false)
	if err != nil {
		return nil, fmt.Errorf("load the CircleCI config: %w", err)
	}
	client, err := authprompt.ResolveCircleCIClient(rc, nil)
	if errors.Is(err, authprompt.ErrNeedsAuth) {
		return nil, newUserError("CircleCI authentication is required to plan with the factory.").
			withCode("auth.circleci_token_required").
			withSuggestion(suggestionCircleCIAuth).
			withExitCode(ExitAuthError).
			withoutDetail()
	}
	if err != nil {
		return nil, fmt.Errorf("connect to CircleCI: %w", err)
	}
	return client, nil
}

// planSession is a factory planning session in root for the planning
// screen: the agent it talks to, which lives as long as ctx, the store its
// plan and comments are kept in through api, and the send of the plan it ends
// with to the factory. The caller must Stop the agent.
func planSession(ctx context.Context, root, model string, api *circleci.Client) (*factoryplan.Agent, plan.Submitter, *factoryplan.Store) {
	store := factoryplan.NewStore(api)
	agent := factoryplan.NewAgent(ctx, factory.AgentConfig{Dir: root, Model: model, SessionID: uuid.NewString()}, store)
	submit := func() (string, error) {
		text, err := store.Ready(ctx)
		if err != nil {
			return "", err
		}
		if err := chunkd.EnsureRunning(); err != nil {
			return "", fmt.Errorf("start the chunk daemon: %w", err)
		}
		return sendPlan(root, store.ID(), text)
	}
	return agent, submit, store
}

// planOrigin is a run's SpecOrigin for factory plan planID, or "" for none.
func planOrigin(planID string) string {
	if planID == "" {
		return ""
	}
	return "factory plan " + planID
}

// sendPlan hands an approved plan, planID in factory, to the factory, as
// 'chunk factory build' would. The run names the plan as where it came from.
func sendPlan(root, planID, text string) (string, error) {
	id, err := chunkd.StartFactory(chunkd.FactoryRequest{
		ProjectRoot: root,
		Prompt:      strings.TrimSpace(text),
		SpecOrigin:  planOrigin(planID),
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

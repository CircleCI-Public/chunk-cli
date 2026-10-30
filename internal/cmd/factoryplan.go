package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

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
	"github.com/CircleCI-Public/chunk-cli/internal/gitremote"
	"github.com/CircleCI-Public/chunk-cli/internal/gitutil"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/planapprove"
	"github.com/CircleCI-Public/chunk-cli/internal/review"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/plan"
)

func newFactoryPlanCmd() *cobra.Command {
	var model string

	cmd := &cobra.Command{
		Use:   "plan [prompt]",
		Short: "Plan a change with Claude, then approve it and build it on the daemon",
		Long: `Plan a change with a factory planning session of Claude Code on this
machine. The request (the prompt, or what you type when there is none)
creates the plan; after that the session works through factory's tasks, and
its activity is shown beside the plan. The plan is kept in factory; comment
on it until it is right, then press ctrl+s to approve it in
factory, and the chunk daemon builds it, in a build session of Claude Code
in the plan's own worktree, through to a pull request. Follow the build in
chunk watch.

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
			if err := checkPlanProject(ctx, root); err != nil {
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
			agent, approve, store := planSession(planCtx, root, "", model, api)
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
			screen := plan.New(planCtx, agent, approve, store, first).
				WithTerminal(lipgloss.HasDarkBackground(os.Stdin, os.Stdout), false).
				WithBranch(branch)
			p := tea.NewProgram(plan.Screen{Model: screen}, tea.WithContext(ctx))
			final, err := p.Run()
			if err != nil {
				return err
			}
			reportPlanLeft(streams, store.ID(), final.(plan.Screen).Model.BuildID)
			return nil
		},
	}

	cmd.Flags().StringVar(&model, "model", "", "model Claude plans and builds with (default: your Claude Code default)")
	return cmd
}

// requireLocalDaemon refuses to run when the environment points at a remote
// daemon. A plan works from files on this machine, so it belongs to the local
// daemon; a remote daemon would be working on a different checkout.
func requireLocalDaemon() error {
	if chunkd.CurrentConnection().Remote == "" {
		return nil
	}
	return newUserError("chunk factory plan works from this machine's files, on the local chunk daemon.").
		withCode("command.invalid_args").
		withSuggestion("Unset CHUNK_DAEMON_REMOTE_ADDR to use this command.").
		withExitCode(ExitBadArgs).
		withoutDetail()
}

// sessionProjectRoot names the project to work on: the git repository
// containing the working directory.
func sessionProjectRoot(ctx context.Context) (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("determine working directory: %w", err)
	}
	top := gitutil.TopLevelCtx(ctx, dir)
	if top == "" {
		return "", newUserError(fmt.Sprintf("%s is not inside a git repository.", dir)).
			withCode("command.invalid_args").
			withExitCode(ExitBadArgs).
			withoutDetail()
	}
	// A build needs an origin to clone and push; refuse before anything starts.
	if _, err := gitremote.URL(ctx, top, "origin"); err != nil {
		return "", newUserError("This project has no git remote named origin.").
			withSuggestion("Add one with: git remote add origin <url>").
			withCode("command.invalid_args").
			withExitCode(ExitBadArgs).
			wrap(err)
	}
	return config.CanonicalProjectRoot(top), nil
}

// checkPlanProject reports why a plan's build cannot start in root, as one
// sentence for the dashboard, or nil when it can.
func checkPlanProject(ctx context.Context, root string) error {
	if _, err := gitremote.URL(ctx, root, "origin"); err != nil {
		return errors.New("this project has no git remote named origin; add one with git remote add origin <url>")
	}
	cfg, err := config.LoadProjectConfig(root)
	if err != nil {
		return errors.New(dashboardMessage(&userError{msg: msgValidateNotConfigured, suggestion: suggestionRunInit, err: err}))
	}
	dir := filepath.Join(root, review.DefaultDir)
	_, _, err = factory.LoadChecks(dir, true, cfg, false)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, factory.ErrNothingToCheck):
		return errors.New(dashboardMessage(&userError{
			msg:        "Nothing for factory to check a plan's build with.",
			suggestion: fmt.Sprintf("Add one .md or .txt file per review to %s, or add validation commands with 'chunk init'.", review.DefaultDir),
		}))
	}
	return errors.New(dashboardMessage(&userError{msg: fmt.Sprintf("Could not read review prompts from %s.", dir), err: err}))
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

// reportPlanLeft says, as the screen closes, where the plan with ID planID
// was left: not approved, or being built as build buildID.
func reportPlanLeft(streams iostream.Streams, planID, buildID string) {
	switch {
	case planID == "":
	case buildID == "":
		streams.ErrPrintf("The plan was not approved. It is plan %s in factory.\n", planID)
	default:
		streams.ErrPrintf("Plan %s is approved and building on the chunk daemon, as run %s.\n", planID, buildID)
		if showHints(streams, false) {
			streams.ErrPrintln(watchStartHint)
		}
	}
}

// planSession is a factory planning session in root for the planning
// screen, on plan id or a new plan when id is "": the agent it talks to,
// which lives as long as ctx, the store its plan and comments are kept in
// through api, and the approval it ends with, which approves the plan in
// factory and has the chunk daemon build it. The caller must Stop the agent.
func planSession(ctx context.Context, root, id, model string, api *circleci.Client) (*factoryplan.Agent, plan.Approver, *factoryplan.Store) {
	store := factoryplan.NewStore(api, id)
	agent := factoryplan.NewAgent(ctx, factory.AgentConfig{Dir: root, Model: model, SessionID: uuid.NewString()}, store)
	approve := func(step func(string)) (string, error) {
		// The daemon is started first, so a plan is not approved with
		// nothing to build it.
		step("Starting the chunk daemon…")
		if err := chunkd.EnsureRunning(); err != nil {
			return "", fmt.Errorf("start the chunk daemon: %w", err)
		}
		return approveAndBuild(step,
			func() error {
				wt, err := factory.PlanWorktreePath(root, store.ID())
				if err != nil {
					return err
				}
				return planapprove.Approve(ctx, api, factory.SessionWorktree, store.ID(), agent.SessionID(), wt)
			},
			agent.Stop,
			func() (string, error) { return buildPlan(root, store.ID(), model) },
		)
	}
	return agent, approve, store
}

// approveAndBuild approves a plan in factory, stops its planning session,
// then has the plan built, returning the build's ID. The planning session is
// stopped as soon as the approval is in, whether or not the build then
// starts: approving released the planning session's claim, or a parent
// planner's, and a planning session still running could
// claim the plan again, when factory would hand it the build tasks, which it
// cannot do. A plan whose approval failed
// keeps its session, to plan on. step is called with each step as it
// starts.
func approveAndBuild(step func(string), approve, stopPlanning func() error, build func() (string, error)) (string, error) {
	step("Approving the plan in factory…")
	if err := approve(); err != nil {
		return "", err
	}
	step("Approved. Stopping the planning session…")
	// The plan is approved, and the session stopped; how it stopped does not
	// change what comes next.
	_ = stopPlanning()
	step("Starting the build on the chunk daemon…")
	return build()
}

// buildPlan has the chunk daemon build approved factory plan planID, in the
// project at root, and returns the build's session ID. A plan it cannot
// start building stays approved in factory, so the error says how to build
// it another way.
func buildPlan(root, planID, model string) (string, error) {
	id, err := chunkd.StartPlanBuild(chunkd.PlanBuildRequest{ProjectRoot: root, PlanID: planID, Model: model})
	var refused *chunkd.SessionRefused
	switch {
	case errors.As(err, &refused):
		err = errors.New(refused.Message)
	case errors.Is(err, chunkd.ErrDaemonUnavailable):
		err = errors.New("the chunk daemon is not reachable")
	}
	if err != nil {
		return "", fmt.Errorf("the plan is approved in factory, but chunk could not start building it (%w); "+
			"build it in Claude Code with /f:resume %s", err, planID)
	}
	return id, nil
}

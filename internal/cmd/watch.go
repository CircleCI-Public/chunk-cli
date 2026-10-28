package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	tea "charm.land/bubbletea/v2"
	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/factoryplan"
	"github.com/CircleCI-Public/chunk-cli/internal/gitutil"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/telemetry"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/flowlist"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/plan"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/watch"
)

// watchCmdName is the name of the watch command, referenced by the update-check
// and auto-launch skip lists.
const watchCmdName = "watch"

func newWatchCmd() *cobra.Command {
	var (
		focus bool
		all   bool
	)

	cmd := &cobra.Command{
		Use:          "watch",
		Short:        "Plan and follow your factory flows",
		SilenceUsage: true,
		Args:         watchArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Checked before the TTY, so the reason it cannot run is the one
			// given wherever it is run.
			if chunkd.CurrentConnection().Remote != "" {
				return newUserError("chunk watch works from this machine, not a remote chunk daemon.").
					withCode("watch.remote_unsupported").
					withSuggestion("Unset CHUNK_DAEMON_REMOTE_ADDR to run chunk watch.").
					withoutDetail()
			}
			if err := ui.RequireStdoutTTY(); err != nil {
				return fmt.Errorf("watch requires a TTY")
			}

			// Every event from this sitting carries its ID, so a funnel can
			// join opening watch to the plans started from it.
			cmd.SetContext(telemetry.WithProperties(cmd.Context(), map[string]any{"watch_session_id": uuid.NewString()}))
			telemetry.Track(cmd.Context(), "watch_opened", nil)

			if err := chunkd.EnsureRunning(); err != nil {
				iostream.FromCmd(cmd).ErrPrintf("chunk watch: daemon unavailable, running without background updates: %v\n", err)
			}

			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("watch: could not determine working directory: %w", err)
			}
			// A plan made from the dashboard is in the project it was opened
			// in; outside one, plans can be read but not chatted in or built.
			root, _ := watchProject(cmd.Context(), cwd)

			// Closing the window stops the planning sessions as quitting does,
			// rather than leaving them running.
			ctx, stopHangup := untilHangup(cmd.Context())
			defer stopHangup()
			plans := newWatchPlans(ctx)
			// Claude runs as a process of its own, which would outlive the
			// dashboard; it is stopped as the dashboard ends.
			defer plans.stop()
			m := watch.New().WithRelaunch().WithBottomMargin(editorRowMargin()).
				WithPlans(plans.ctx, root, plans.plan).
				WithFlowList(flowlist.New(plans.ctx, listFactoryFlows, previewFactoryPlan).WithParents(listFactoryParents))
			p := tea.NewProgram(m, tea.WithContext(ctx))
			final, err := p.Run()
			if err != nil {
				return err
			}
			if fm, ok := final.(watch.Model); ok && fm.HasPlan() {
				iostream.FromCmd(cmd).ErrPrintln("Plans and saved comments remain in Factory. Reopen them with chunk watch; local activity and unsent input are not kept after quitting.")
			}
			return nil
		},
	}

	// Deprecated no-ops: flows are the signed-in user's wherever chunk watch is
	// run. They still parse so existing invocations keep working.
	cmd.Flags().BoolVar(&focus, "focus", false, "Watch only the current directory instead of all known projects")
	cmd.Flags().BoolVar(&all, "all", false, "Watch all known projects (default)")
	_ = cmd.Flags().MarkDeprecated("focus", "chunk watch shows your factory flows wherever it is run")
	_ = cmd.Flags().MarkDeprecated("all", "chunk watch shows your factory flows wherever it is run")
	return cmd
}

// watchArgs refuses arguments, as cobra.NoArgs does, with the exit code for
// bad arguments.
func watchArgs(_ *cobra.Command, args []string) error {
	if len(args) == 0 {
		return nil
	}
	return newUserError(fmt.Sprintf("chunk watch takes no arguments, got %q.", args[0])).
		withCode("command.invalid_args").
		withSuggestion("Run chunk watch in the project to plan in; it shows your factory flows wherever it is run.").
		withExitCode(ExitBadArgs).
		withoutDetail()
}

// watchProject is the project chunk watch was opened in: the canonical
// git top level of cwd, registered so the daemon finds it. ok is false,
// and nothing is registered, outside a git repository.
func watchProject(ctx context.Context, cwd string) (root string, ok bool) {
	top := gitutil.TopLevelCtx(ctx, cwd)
	if top == "" {
		return "", false
	}
	// Canonical because the daemon keys projects by the canonical spelling.
	// Git happens to answer with a resolved path on darwin, so a mismatch
	// would be invisible on one platform only.
	root = config.CanonicalProjectRoot(top)
	if dataDir, err := config.ProjectDataDir(root); err == nil {
		_ = sidecar.RegisterProjectRoot(dataDir, root)
	}
	return root, true
}

// editorRowMargin is how many of the terminal's last rows the dashboard leaves
// blank. The built-in terminals of VS Code (and the editors built on it, such
// as Cursor and Windsurf, which name themselves the same way) and of JetBrains
// IDEs can report a couple of rows more than their panel shows, which hid the
// footer below the panel's edge.
func editorRowMargin() int {
	if os.Getenv("TERM_PROGRAM") == "vscode" || os.Getenv("TERMINAL_EMULATOR") == "JetBrains-JediTerm" {
		return 2
	}
	return 0
}

// watchPlans opens the plans the dashboard asks for, and stops their agents
// when it ends.
type watchPlans struct {
	ctx    context.Context
	cancel context.CancelFunc

	mu sync.Mutex
	// planAgents are the agents of every plan opened from the dashboard,
	// stopped as it closes. Approving a plan stops its agent already.
	planAgents []*factoryplan.Agent
}

func newWatchPlans(parent context.Context) *watchPlans {
	ctx, cancel := context.WithCancel(parent)
	return &watchPlans{ctx: ctx, cancel: cancel}
}

func (d *watchPlans) stop() {
	d.cancel()
	d.mu.Lock()
	agents := append([]*factoryplan.Agent(nil), d.planAgents...)
	d.mu.Unlock()
	for _, agent := range agents {
		_ = agent.Stop()
	}
}

// plan opens an existing plan or a new workspace. Reading requires only the
// API; local project, daemon and claim checks happen when starting an agent.
// Approving the plan stops its session.
func (d *watchPlans) plan(ctx context.Context, root, id string) (plan.Agent, plan.Approver, plan.Store, error) {
	api, err := factoryPlanClient()
	if err != nil {
		return nil, nil, nil, err
	}
	if id != "" {
		if _, err := api.GetPlan(ctx, id); err != nil {
			return nil, nil, nil, fmt.Errorf("open plan: %w", err)
		}
	}
	agent, build, store := planSession(d.ctx, root, id, "", api)
	agent.CheckStart(func(ctx context.Context) error {
		holder, err := store.Holder(ctx)
		if err != nil {
			return err
		}
		if holder != "" && holder != agent.SessionID() {
			return errors.New("this plan is claimed by another factory session; you can read and comment without taking its claim")
		}
		if err := watchPlanProject(ctx, root); err != nil {
			return err
		}
		if err := factory.CheckClaude(); err != nil {
			return errors.New("this machine has no Claude Code to plan with; install it from https://claude.com/claude-code")
		}
		if err := factory.CheckFactoryDaemon(ctx); err != nil {
			return watchFactoryProblem(err)
		}
		return nil
	})
	approve := func(step func(string)) (string, error) {
		if err := watchPlanProject(ctx, root); err != nil {
			return "", err
		}
		return build(step)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.ctx.Err(); err != nil {
		_ = agent.Stop()
		return nil, nil, nil, err
	}
	d.planAgents = append(d.planAgents, agent)
	return agent, approve, store, nil
}

func watchPlanProject(ctx context.Context, root string) error {
	if root == "" {
		return errors.New("open chunk watch in this plan's local repository to plan or build")
	}
	return checkPlanProject(ctx, root)
}

// listFactoryFlows is the caller's open flows, for the flow list.
func listFactoryFlows(ctx context.Context) ([]circleci.Flow, error) {
	api, err := factoryPlanClient()
	if err != nil {
		return nil, err
	}
	flows, err := api.ListFlows(ctx)
	if err != nil {
		return nil, err
	}
	return openFlows(flows), nil
}

// openFlows is flows without the ended ones. A flow from a conductor that
// serves no phase is kept.
func openFlows(flows []circleci.Flow) []circleci.Flow {
	out := make([]circleci.Flow, 0, len(flows))
	for _, f := range flows {
		if f.Phase != circleci.PhaseEnded {
			out = append(out, f)
		}
	}
	return out
}

// listFactoryParents is each open sub-plan's parent, by plan ID, for the
// flow list.
func listFactoryParents(ctx context.Context) (map[string]string, error) {
	api, err := factoryPlanClient()
	if err != nil {
		return nil, fmt.Errorf("connect to factory: %w", err)
	}
	plans, err := api.ListPlans(ctx)
	if err != nil {
		return nil, fmt.Errorf("list plans: %w", err)
	}
	return planParents(plans), nil
}

// planParents is each of plans that has a parent, mapped to it.
func planParents(plans []circleci.Plan) map[string]string {
	out := make(map[string]string)
	for _, p := range plans {
		if p.Parent != "" {
			out[p.ID] = p.Parent
		}
	}
	return out
}

func previewFactoryPlan(ctx context.Context, id string) (string, []circleci.PlanComment, error) {
	api, err := factoryPlanClient()
	if err != nil {
		return "", nil, err
	}
	text, err := api.PlanText(ctx, id)
	if err != nil {
		return "", nil, err
	}
	comments, err := api.ListComments(ctx, id, true)
	return text, comments, err
}

// watchFactoryProblem is CheckFactoryDaemon's error as one sentence for the
// dashboard.
func watchFactoryProblem(err error) error {
	switch {
	case errors.Is(err, factory.ErrNoFactory):
		return errors.New("factory is not installed; install it and put it on PATH to plan")
	case errors.Is(err, factory.ErrNoFactoryDaemon):
		return errors.New("the factory daemon is not running; run `factory daemon` with FACTORY_PLAN_STORE=circleci to plan")
	}
	return err
}

// dashboardMessage is err as the dashboard shows it: a user error's message
// and suggestion on one line, or the error itself.
func dashboardMessage(err error) string {
	ue, ok := errors.AsType[*userError](err)
	if !ok {
		return err.Error()
	}
	msg := strings.TrimSuffix(ue.UserMessage(), ".")
	if s := ue.Suggestion(); s != "" {
		msg += ". " + s
	}
	return msg
}

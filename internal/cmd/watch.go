package cmd

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	tea "charm.land/bubbletea/v2"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/factoryplan"
	"github.com/CircleCI-Public/chunk-cli/internal/gitutil"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/planstate"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/telemetry"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/chat"
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
				WithPlanBuilds(daemonPlanBuild{}).
				WithPlanResume(daemonResume{}).
				WithPlanDelete(factoryDelete{}).
				WithFlowList(flowlist.New(plans.ctx, listFactoryFlows, previewFactoryPlan).
					WithParents(listFactoryParents).WithBlockers(listFactoryBlockers)).
				WithChat(newChatModel(plans.ctx, root))
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
	// whose polling stops as it closes.
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
// API; the local project is checked before a request, resume or approval.
// The agent's polling stops as the dashboard closes.
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
		return watchPlanProject(ctx, root)
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

// newChatModel makes the chat, talking to root's chat session in the
// local chunk daemon; root is "" outside a repository.
func newChatModel(ctx context.Context, root string) func() chat.Model {
	return func() chat.Model {
		switch {
		case chunkd.CurrentConnection().Remote != "":
			return chat.Unavailable("Chat runs on this machine's chunk daemon, not a remote one.")
		case root == "":
			return chat.Unavailable("Open chunk watch in a repository to use chat.")
		}
		send := func(_ context.Context, text, screen string) error {
			// The log it answers with is read by the next poll.
			_, err := chunkd.SendChat(chunkd.ChatMessage{Root: root, Text: text, Context: screen})
			return err
		}
		poll := func(_ context.Context, after int) (chat.Update, error) {
			l, err := chunkd.FetchChat(root, after)
			if err != nil {
				return chat.Update{}, fmt.Errorf("read the chat: %w", err)
			}
			return chatUpdate(l), nil
		}
		return chat.New(ctx, send, poll)
	}
}

// chatUpdate is a chat session's log as the chat shows it. An event of a kind
// the chat has no place for is left out.
func chatUpdate(l chunkd.SessionLog) chat.Update {
	u := chat.Update{LogID: l.LogID, Next: l.Next, Busy: l.Busy, Activity: l.Activity}
	for _, e := range l.Events {
		switch e.Kind {
		case chunkd.SessionLogMessage:
			u.Entries = append(u.Entries, chat.Entry{Message: e.Text})
		case chunkd.SessionLogActivity:
			u.Entries = append(u.Entries, chat.Entry{Activity: e.Text})
		case chunkd.SessionLogReply:
			u.Entries = append(u.Entries, chat.Entry{Reply: e.Text})
		case chunkd.SessionLogProblem:
			u.Entries = append(u.Entries, chat.Entry{Problem: e.Text})
		}
	}
	return u
}

// daemonPlanBuild reads plan builds from the local chunk daemon for the flow
// screen.
type daemonPlanBuild struct{}

// Activity reads plan planID's build.
func (daemonPlanBuild) Activity(_ context.Context, planID string, after int) (plan.BuildActivity, error) {
	a, found, err := chunkd.FetchPlanBuild(planID, after)
	if errors.Is(err, chunkd.ErrDaemonUnavailable) {
		return plan.BuildActivity{}, fmt.Errorf("the chunk daemon is not reachable, or is too old to show builds: %w", err)
	}
	if err != nil {
		return plan.BuildActivity{}, fmt.Errorf("read the build of plan %s: %w", planID, err)
	}
	return planBuildFrom(a, found), nil
}

// planBuildFrom is the daemon's answer as the flow screen reads it.
func planBuildFrom(a chunkd.PlanBuildActivity, found bool) plan.BuildActivity {
	if !found {
		return plan.BuildActivity{}
	}
	out := plan.BuildActivity{
		Found:   true,
		ID:      a.ID,
		Running: a.State == chunkd.SessionRunning,
		First:   a.First,
		Total:   a.Total,
	}
	if a.State == chunkd.SessionFailed {
		out.Failed = a.Error
	}
	if len(a.Lines) > 0 {
		out.Lines = make([]plan.BuildLine, len(a.Lines))
		for i, l := range a.Lines {
			out.Lines[i] = plan.BuildLine{Level: planBuildLevel(l.Level), Text: l.Text}
		}
	}
	return out
}

// planBuildLevel is a feed line's level as the flow screen styles it. An
// unknown level, from a newer daemon, reads as info.
func planBuildLevel(l chunkd.FeedLevel) plan.BuildLevel {
	switch l {
	case chunkd.FeedStep, chunkd.FeedDone:
		return plan.BuildStep
	case chunkd.FeedWarn:
		return plan.BuildWarn
	case chunkd.FeedError:
		return plan.BuildError
	case chunkd.FeedInfo:
	}
	return plan.BuildInfo
}

func watchPlanProject(ctx context.Context, root string) error {
	if root == "" {
		return errors.New("open chunk watch in this plan's local repository to plan or build")
	}
	return checkPlanProject(ctx, root)
}

// checkWatchProject, resumePlanner and startPlanBuild are watchPlanProject
// and the chunk daemon's routes but in tests.
var (
	checkWatchProject = watchPlanProject
	resumePlanner     = chunkd.ResumePlanner
	startPlanBuild    = chunkd.StartPlanBuild
)

// errNothingToResume is a resume of a kind chunk does not start.
var errNothingToResume = errors.New("this plan has nothing for chunk to resume")

// daemonResume starts plans' agents again in the local chunk daemon.
type daemonResume struct{}

// Resume has plan planID's planner claim it again, or starts a build session
// of it, as kind says, in the project at root.
func (daemonResume) Resume(ctx context.Context, root, planID string, kind planstate.ResumeKind) error {
	if err := checkWatchProject(ctx, root); err != nil {
		return err
	}
	var err error
	switch kind {
	case planstate.ResumePlanner:
		_, err = resumePlanner(planID, chunkd.PlannerResume{ProjectRoot: root})
	case planstate.ResumeBuild:
		_, err = startPlanBuild(chunkd.PlanBuildRequest{ProjectRoot: root, PlanID: planID})
	case planstate.ResumeNone:
		return errNothingToResume
	default:
		return errNothingToResume
	}
	return resumeProblem(err)
}

// factoryDelete deletes plans in factory. It needs no project root.
type factoryDelete struct {
	// client makes the factory client; factoryPlanClient when nil.
	client func() (*circleci.Client, error)
}

// Delete deletes plan planID.
func (d factoryDelete) Delete(ctx context.Context, planID string) error {
	newClient := d.client
	if newClient == nil {
		newClient = factoryPlanClient
	}
	api, err := newClient()
	if err != nil {
		return err
	}
	return api.DeletePlan(ctx, planID)
}

// resumeProblem is a failed resume as the dashboard shows it: the daemon's
// refusal as it gives it.
func resumeProblem(err error) error {
	if refused, ok := errors.AsType[*chunkd.SessionRefused](err); ok {
		return errors.New(refused.Message)
	}
	if errors.Is(err, chunkd.ErrDaemonUnavailable) {
		return errors.New("the chunk daemon is not reachable")
	}
	return err
}

// listFactoryBlockers is the plans blocking plan id, each with its flow's
// state and outcome, for the flow list.
func listFactoryBlockers(ctx context.Context, id string) ([]planstate.Blocker, error) {
	api, err := factoryPlanClient()
	if err != nil {
		return nil, err
	}
	blockers, err := api.ListFlowBlockers(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("list the plan's blockers: %w", err)
	}
	flows, err := api.ListFlows(ctx)
	if err != nil {
		return nil, fmt.Errorf("list the plans' flows: %w", err)
	}
	return blockerStates(blockers, flows), nil
}

// blockerStates is blockers, with state and outcome taken from their listed
// flows. The blockers endpoint serves no outcome.
func blockerStates(blockers []circleci.Blocker, flows []circleci.Flow) []planstate.Blocker {
	byID := make(map[string]circleci.Flow, len(flows))
	for _, f := range flows {
		byID[f.ID] = f
	}
	out := make([]planstate.Blocker, len(blockers))
	for i, b := range blockers {
		out[i] = planstate.Blocker{Title: b.Title, State: b.State}
		if f, ok := byID[b.ID]; ok {
			out[i] = planstate.Blocker{Title: cmp.Or(f.Title, b.Title), State: f.State, Outcome: f.Outcome}
		}
	}
	return out
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
	return previewPlan(ctx, api, id)
}

// previewPlan is plan id's text and open comments, fetched at the same time.
func previewPlan(ctx context.Context, api *circleci.Client, id string) (string, []circleci.PlanComment, error) {
	g, ctx := errgroup.WithContext(ctx)
	var text string
	var comments []circleci.PlanComment
	g.Go(func() error {
		var err error
		if text, err = api.PlanText(ctx, id); err != nil {
			return fmt.Errorf("plan text: %w", err)
		}
		return nil
	})
	g.Go(func() error {
		var err error
		if comments, err = api.ListComments(ctx, id, true); err != nil {
			return fmt.Errorf("list comments: %w", err)
		}
		return nil
	})
	if err := g.Wait(); err != nil {
		return "", nil, err
	}
	return text, comments, nil
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

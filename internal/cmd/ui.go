package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

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

// uiCmdName is the name of the ui command, referenced by the update-check
// and auto-launch skip lists.
const uiCmdName = "ui"

func newUICmd() *cobra.Command {
	var (
		focus bool
		all   bool
	)

	cmd := &cobra.Command{
		Use:          "ui",
		Aliases:      []string{"watch"},
		Short:        "Plan and follow your factory flows",
		SilenceUsage: true,
		Args:         watchArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Checked before the TTY, so the reason it cannot run is the one
			// given wherever it is run.
			if chunkd.CurrentConnection().Remote != "" {
				return newUserError("chunk ui works from this machine, not a remote chunk daemon.").
					withCode("ui.remote_unsupported").
					withSuggestion("Unset CHUNK_DAEMON_REMOTE_ADDR to run chunk ui.").
					withoutDetail()
			}
			if err := ui.RequireStdoutTTY(); err != nil {
				return fmt.Errorf("ui requires a TTY")
			}

			// Every event from this sitting carries its ID, so a funnel can
			// join opening watch to the plans started from it.
			cmd.SetContext(telemetry.WithProperties(cmd.Context(), map[string]any{"watch_session_id": uuid.NewString()}))
			telemetry.Track(cmd.Context(), "watch_opened", nil)

			if err := chunkd.EnsureRunning(); err != nil {
				iostream.FromCmd(cmd).ErrPrintf("chunk ui: daemon unavailable, running without background updates: %v\n", err)
			}

			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("watch: could not determine working directory: %w", err)
			}
			// A plan made from the dashboard is in the project it was opened
			// in. Outside one, new plans cannot be made nor the chat used.
			root, _ := watchProject(cmd.Context(), cwd)

			// Closing the window ends the dashboard as quitting does.
			ctx, stopHangup := untilHangup(cmd.Context())
			defer stopHangup()
			plans := newWatchPlans(ctx)
			defer plans.stop()
			m := watch.New().WithRelaunch().WithBottomMargin(editorRowMargin()).
				WithPlans(plans.ctx, root, plans.plan).
				WithPlanClaim(claimPlan).
				WithPlanDelete(factoryDelete{}).
				WithFlowList(flowlist.New(plans.ctx, listFactoryFlows, previewFactoryPlan).
					WithParents(listFactoryParents).WithBlockers(listFactoryBlockers).
					WithWorkers(readFactoryWorker)).
				WithChat(newChatModel(plans.ctx, root))
			p := tea.NewProgram(m, tea.WithContext(ctx))
			final, err := p.Run()
			if err != nil {
				return err
			}
			if fm, ok := final.(watch.Model); ok && fm.HasPlan() {
				iostream.FromCmd(cmd).ErrPrintln("Plans and saved comments remain in Factory. Reopen them with chunk ui; local activity and unsent input are not kept after quitting.")
			}
			return nil
		},
	}

	// Deprecated no-ops: flows are the signed-in user's wherever chunk ui is
	// run. They still parse so existing invocations keep working.
	cmd.Flags().BoolVar(&focus, "focus", false, "Watch only the current directory instead of all known projects")
	cmd.Flags().BoolVar(&all, "all", false, "Watch all known projects (default)")
	_ = cmd.Flags().MarkDeprecated("focus", "chunk ui shows your factory flows wherever it is run")
	_ = cmd.Flags().MarkDeprecated("all", "chunk ui shows your factory flows wherever it is run")
	return cmd
}

// watchArgs refuses arguments, as cobra.NoArgs does, with the exit code for
// bad arguments.
func watchArgs(_ *cobra.Command, args []string) error {
	if len(args) == 0 {
		return nil
	}
	return newUserError(fmt.Sprintf("chunk ui takes no arguments, got %q.", args[0])).
		withCode("command.invalid_args").
		withSuggestion("Run chunk ui in the project to plan in; it shows your factory flows wherever it is run.").
		withExitCode(ExitBadArgs).
		withoutDetail()
}

// watchProject is the project chunk ui was opened in: the canonical
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

// watchPlans opens the plans the dashboard asks for, and cancels what they
// read as it ends.
type watchPlans struct {
	ctx    context.Context
	cancel context.CancelFunc

	// client makes the factory client; factoryPlanClient when nil.
	client func() (*circleci.Client, error)
}

func newWatchPlans(parent context.Context) *watchPlans {
	ctx, cancel := context.WithCancel(parent)
	return &watchPlans{ctx: ctx, cancel: cancel}
}

func (d *watchPlans) stop() { d.cancel() }

// plan opens an existing plan or a new workspace. An existing plan needs only
// the API, whether or not this machine has a checkout of its repo. A new one
// is made in root, the project the dashboard was opened in, which is checked
// before the screen opens, so a failure shows as the dashboard's note.
func (d *watchPlans) plan(ctx context.Context, root, id string) (watch.OpenedPlan, error) {
	newClient := d.client
	if newClient == nil {
		newClient = factoryPlanClient
	}
	api, err := newClient()
	if err != nil {
		return watch.OpenedPlan{}, err
	}
	if id != "" {
		return watch.OpenedPlan{Store: factoryplan.NewStore(api, id, "")}, nil
	}
	if err := checkWatchProject(ctx, root); err != nil {
		return watch.OpenedPlan{}, err
	}
	return watch.OpenedPlan{Store: factoryplan.NewStore(api, "", planRepo(ctx, root)), Root: root}, nil
}

// claimPlan has the default worker claim plan planID, and returns the worker's
// hostname, for the flow list's R.
func claimPlan(ctx context.Context, planID string) (string, error) {
	api, err := factoryPlanClient()
	if err != nil {
		return "", err
	}
	return factoryplan.NewStore(api, planID, "").Claim(ctx)
}

// readFactoryWorker is the worker that claims flow flowID, as it reports the
// flow, for the flow list; nil when no worker claims it.
func readFactoryWorker(ctx context.Context, flowID string) (*plan.Worker, error) {
	api, err := factoryPlanClient()
	if err != nil {
		return nil, err
	}
	return readWorker(ctx, api, flowID)
}

// readWorker reads who claims flow flowID, then that worker as it reports the
// flow. The list only has the claim's worker ID, which it may since have lost.
func readWorker(ctx context.Context, api *circleci.Client, flowID string) (*plan.Worker, error) {
	f, err := api.GetFlow(ctx, flowID)
	if err != nil {
		return nil, fmt.Errorf("get the plan's flow: %w", err)
	}
	return factoryplan.WorkerOf(ctx, api, flowID, f.WorkerID), nil
}

// newChatModel makes the chat, talking to root's chat session in the
// local chunk daemon; root is "" outside a repository.
func newChatModel(ctx context.Context, root string) func() chat.Model {
	return func() chat.Model {
		switch {
		case chunkd.CurrentConnection().Remote != "":
			return chat.Unavailable("Chat runs on this machine's chunk daemon, not a remote one.")
		case root == "":
			return chat.Unavailable("Open chunk ui in a repository to use chat.")
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

// watchPlanProject is the check of the project a new plan is made in: that
// the dashboard was opened in one, and that it can be built from.
func watchPlanProject(ctx context.Context, root string) error {
	if root == "" {
		return errors.New("open chunk ui in a local repository to make a plan")
	}
	return checkPlanProject(ctx, root)
}

// checkWatchProject is the project check, but in tests.
var checkWatchProject = watchPlanProject

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
	return blockerStates(blockers), nil
}

// blockerStates is blockers as the flow list reads them.
func blockerStates(blockers []circleci.Blocker) []planstate.Blocker {
	out := make([]planstate.Blocker, len(blockers))
	for i, b := range blockers {
		out[i] = planstate.Blocker{Title: b.Title, State: b.State, Outcome: b.Outcome, Active: b.Active}
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

// listFactoryParents is each sub-plan's parent, by plan ID, for the flow
// list, as the flows list serves it.
func listFactoryParents(ctx context.Context) (map[string]string, error) {
	api, err := factoryPlanClient()
	if err != nil {
		return nil, fmt.Errorf("connect to factory: %w", err)
	}
	flows, err := api.ListFlows(ctx)
	if err != nil {
		return nil, fmt.Errorf("list flows: %w", err)
	}
	return planParents(flows), nil
}

// planParents is the plan of each of flows that has a parent, mapped to it.
func planParents(flows []circleci.Flow) map[string]string {
	out := make(map[string]string)
	for _, f := range flows {
		if f.Parent != "" {
			out[f.ID] = f.Parent
		}
	}
	return out
}

func previewFactoryPlan(ctx context.Context, id string) (plan.Structure, []circleci.PlanComment, error) {
	api, err := factoryPlanClient()
	if err != nil {
		return plan.Structure{}, nil, err
	}
	return previewPlan(ctx, api, id)
}

// previewPlan is plan id as factory's structured content holds it, and its
// open comments, fetched at the same time.
func previewPlan(ctx context.Context, api *circleci.Client, id string) (plan.Structure, []circleci.PlanComment, error) {
	g, ctx := errgroup.WithContext(ctx)
	var structure plan.Structure
	var comments []circleci.PlanComment
	g.Go(func() error {
		sc, err := api.PlanStructuredContent(ctx, id)
		if err != nil {
			return fmt.Errorf("plan structured content: %w", err)
		}
		structure = factoryplan.StructureOf(sc)
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
		return plan.Structure{}, nil, err
	}
	return structure, comments, nil
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

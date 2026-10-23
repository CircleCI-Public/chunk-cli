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

	"github.com/CircleCI-Public/chunk-cli/internal/authprompt"
	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/claudecode"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
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
			api, err := factoryPlanClient()
			if err != nil {
				return err
			}

			turn, submit, store := planSession(root, planPath, model, api)

			var first string
			if len(args) == 1 {
				first = args[0]
			}
			// Claude runs as a process of its own; this context stops it if
			// the screen is closed mid-turn.
			planCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			// The branch is only for the header; a repo without one has none.
			branch, _ := gitutil.CurrentBranchIn(root)
			screen := plan.New(planCtx, turn, submit, store, first).
				WithTerminal(lipgloss.HasDarkBackground(os.Stdin, os.Stdout), false).
				WithBranch(branch)
			p := tea.NewProgram(plan.Screen{Model: screen}, tea.WithContext(ctx))
			final, err := p.Run()
			if err != nil {
				return err
			}
			m := final.(plan.Screen).Model
			if m.RunID == "" {
				if id := storePlanID(store); id != "" {
					streams.ErrPrintf("The plan was not sent. It is plan %s in factory; open 'chunk factory plan' again to keep working on it.\n", id)
				}
				return nil
			}
			streams.ErrPrintf("Factory run %s started on the chunk daemon from %s.\n", m.RunID, planPath)
			if showHints(streams, false) {
				streams.ErrPrintln(watchStartHint)
			}
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

// planSession is a conversation with Claude about the plan at planPath, in
// root, for the planning screen: its turns, the store its plan and comments
// are kept in through api, and the send of the plan it ends with to the
// factory.
func planSession(root, planPath, model string, api *circleci.Client) (plan.Turner, plan.Submitter, plan.Store) {
	session := &factory.PlanSession{Dir: root, PlanPath: planPath, Model: model}
	claimed := false
	turn := func(ctx context.Context, message string, activity func(string)) (string, error) {
		message, claimed = withPlanClaim(claimed, session.PlanID, message)
		return session.Turn(ctx, message, func(a claudecode.Activity) { activity(activityLine(a)) })
	}
	submit := func(text string) (string, error) {
		if err := chunkd.EnsureRunning(); err != nil {
			return "", fmt.Errorf("start the chunk daemon: %w", err)
		}
		return sendPlan(root, planPath, text)
	}
	return turn, submit, &apiStore{api: api, session: session}
}

// withPlanClaim prepends the plan-claim instruction to message when it is
// the first message sent since planID was created, so the agent can claim
// the plan; a later message, or one sent before the plan exists, passes
// through unchanged. claimed is carried by the caller across calls.
func withPlanClaim(claimed bool, planID, message string) (string, bool) {
	if claimed || planID == "" {
		return message, claimed
	}
	return fmt.Sprintf("Plan %s in factory is for this conversation; claim it with plan_claim.\n\n%s",
		planID, message), true
}

// storePlanID is the plan's ID in factory, once an apiStore has created one,
// or "" for any other Store (including one that never got that far).
func storePlanID(s plan.Store) string {
	as, ok := s.(*apiStore)
	if !ok {
		return ""
	}
	return as.session.PlanID
}

// apiStore is a plan.Store backed by factory's V3 plan and comment API,
// addressed by the plan's ID on session, which Create sets. The plan's text
// is read from the local file Claude writes it to during this per-turn
// session rather than from factory, which the TUI never writes to: nothing
// updates it there until the long-lived agent session this plan does not
// build exists to call plan_set, so reading it from there would show
// nothing. That local read goes away with PlanPath once that session lands.
type apiStore struct {
	api     *circleci.Client
	session *factory.PlanSession
}

func (s *apiStore) Create(ctx context.Context, title string) error {
	p, err := s.api.CreatePlan(ctx, title, "")
	if err != nil {
		return fmt.Errorf("create the plan in factory: %w", err)
	}
	s.session.PlanID = p.ID
	return nil
}

// errPlanNotCreated is returned by a write before Create has set an ID to
// address it with; every write happens after a send, which creates the plan
// first, so this only guards against a caller skipping that order.
var errPlanNotCreated = errors.New("the plan has not been created in factory yet")

func (s *apiStore) State(ctx context.Context) (string, error) {
	if s.session.PlanID == "" {
		return "", nil
	}
	p, err := s.api.GetPlan(ctx, s.session.PlanID)
	if err != nil {
		return "", fmt.Errorf("get the plan's state: %w", err)
	}
	return p.State, nil
}

func (s *apiStore) Move(ctx context.Context, state string) error {
	if s.session.PlanID == "" {
		return errPlanNotCreated
	}
	if _, err := s.api.MovePlan(ctx, s.session.PlanID, state); err != nil {
		return fmt.Errorf("move the plan to %s: %w", state, err)
	}
	return nil
}

func (s *apiStore) Text(context.Context) (string, error) { return s.session.Plan() }

func (s *apiStore) Comments(ctx context.Context) ([]plan.Comment, error) {
	if s.session.PlanID == "" {
		return nil, nil
	}
	cs, err := s.api.ListComments(ctx, s.session.PlanID, false)
	if err != nil {
		return nil, fmt.Errorf("list the plan's comments: %w", err)
	}
	out := make([]plan.Comment, len(cs))
	for i, c := range cs {
		out[i] = planComment(c)
	}
	return out, nil
}

func (s *apiStore) AddComment(ctx context.Context, quote, body string) (plan.Comment, error) {
	if s.session.PlanID == "" {
		return plan.Comment{}, errPlanNotCreated
	}
	c, err := s.api.CreateComment(ctx, s.session.PlanID, quote, body)
	if err != nil {
		return plan.Comment{}, fmt.Errorf("save the comment: %w", err)
	}
	return planComment(*c), nil
}

func planComment(c circleci.PlanComment) plan.Comment {
	return plan.Comment{ID: c.ID, Quote: c.QuotedText, Body: c.Body, Resolved: c.ResolvedAt != nil}
}

func (s *apiStore) EditComment(ctx context.Context, id, body string) error {
	if err := s.api.SetCommentBody(ctx, id, body); err != nil {
		return fmt.Errorf("save the comment: %w", err)
	}
	return nil
}

func (s *apiStore) ResolveComment(ctx context.Context, id string) error {
	if _, err := s.api.ResolveComment(ctx, id); err != nil {
		return fmt.Errorf("resolve the comment: %w", err)
	}
	return nil
}

func (s *apiStore) DeleteComment(ctx context.Context, id string) error {
	if err := s.api.DeleteComment(ctx, id); err != nil {
		return fmt.Errorf("delete the comment: %w", err)
	}
	return nil
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

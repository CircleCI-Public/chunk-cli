package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/authprompt"
	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
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
				if id := store.id(); id != "" {
					streams.ErrPrintf("The plan was not sent. It is plan %s in factory.\n", id)
				}
				return nil
			}
			streams.ErrPrintf("Factory run %s started on the chunk daemon from plan %s in factory.\n", m.RunID, store.id())
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
func planSession(ctx context.Context, root, model string, api *circleci.Client) (*planAgent, plan.Submitter, *apiStore) {
	store := &apiStore{api: api}
	agent := newPlanAgent(ctx, factory.AgentConfig{Dir: root, Model: model, SessionID: uuid.NewString()}, store)
	submit := func(text string) (string, error) {
		if err := chunkd.EnsureRunning(); err != nil {
			return "", fmt.Errorf("start the chunk daemon: %w", err)
		}
		return sendPlan(root, text)
	}
	return agent, submit, store
}

// planClaim is what the first message after the plan is created starts
// with. Claiming alone leaves the agent scouting, as factory.md has it until
// a change is asked for, so it is told the conversation is planning, and how
// the plan gets written: by the daemon's tasks, not in the chat.
const planClaim = "This conversation plans a change through factory, as plan %s. " +
	"Claim it with plan_claim, then run `factory daemon wait` in the background and do the planning tasks it hands out. " +
	"The plan is written with the plan tools, never in your replies; keep replies short. " +
	"The developer's request:\n\n%s"

// withPlanClaim prepends planClaim to message when it is the first message
// sent since planID was created, so the agent claims the plan and starts
// planning it; a later message, or one sent before the plan exists, passes
// through unchanged. claimed is carried by the caller across calls.
func withPlanClaim(claimed bool, planID, message string) (string, bool) {
	if claimed || planID == "" {
		return message, claimed
	}
	return fmt.Sprintf(planClaim, planID, message), true
}

// apiStore is a plan.Store backed by factory's V3 plan and comment API,
// addressed by the plan's ID, which Create sets. The TUI only reads the
// plan's text: the agent writes it, with plan_set through the factory daemon.
type apiStore struct {
	api *circleci.Client

	mu     sync.Mutex
	planID string
}

// id is the plan's ID in factory, or "" before Create.
func (s *apiStore) id() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.planID
}

// planSkeleton is the empty plan factory's plan_init writes (factory's
// plan/plan.go). The plan tools edit a plan by its sections, so a plan
// created without them gives the agent nothing to write into.
func planSkeleton(title string) string {
	return "# " + title + "\n\n## 1. Outcome\n\n## 2. Design\n\n## 3. Open Questions\n\n## 4. Worked example\n\n## 5. Units\n"
}

func (s *apiStore) Create(ctx context.Context, title string) error {
	p, err := s.api.CreatePlan(ctx, title, planSkeleton(title))
	if err != nil {
		return fmt.Errorf("create the plan in factory: %w", err)
	}
	s.mu.Lock()
	s.planID = p.ID
	s.mu.Unlock()
	return nil
}

// errPlanNotCreated is returned by a write before Create has set an ID to
// address it with; every write happens after a send, which creates the plan
// first, so this only guards against a caller skipping that order.
var errPlanNotCreated = errors.New("the plan has not been created in factory yet")

func (s *apiStore) State(ctx context.Context) (string, error) {
	id := s.id()
	if id == "" {
		return "", nil
	}
	p, err := s.api.GetPlan(ctx, id)
	if err != nil {
		return "", fmt.Errorf("get the plan's state: %w", err)
	}
	return p.State, nil
}

func (s *apiStore) Move(ctx context.Context, state string) error {
	id := s.id()
	if id == "" {
		return errPlanNotCreated
	}
	if _, err := s.api.MovePlan(ctx, id, state); err != nil {
		return fmt.Errorf("move the plan to %s: %w", state, err)
	}
	return nil
}

func (s *apiStore) Text(ctx context.Context) (string, error) {
	id := s.id()
	if id == "" {
		return "", nil
	}
	text, err := s.api.PlanText(ctx, id)
	if err != nil {
		return "", fmt.Errorf("get the plan's text: %w", err)
	}
	return text, nil
}

func (s *apiStore) Comments(ctx context.Context) ([]plan.Comment, error) {
	id := s.id()
	if id == "" {
		return nil, nil
	}
	cs, err := s.api.ListComments(ctx, id, false)
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
	id := s.id()
	if id == "" {
		return plan.Comment{}, errPlanNotCreated
	}
	c, err := s.api.CreateComment(ctx, id, quote, body)
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

// sendPlan hands an approved plan to the factory, as 'chunk factory build'
// would.
func sendPlan(root, text string) (string, error) {
	id, err := chunkd.StartFactory(chunkd.FactoryRequest{
		ProjectRoot: root,
		Prompt:      strings.TrimSpace(text),
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

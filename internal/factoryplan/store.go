// Package factoryplan backs the planning screen with factory: the plan and its
// comments in factory's V3 API (Store), and the planner the chunk daemon runs
// for it (Agent).
package factoryplan

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/planapprove"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/plan"
)

// Store is a plan.Store backed by factory's V3 plan and comment API,
// addressed by the plan's ID, which Create sets.
type Store struct {
	api *circleci.Client
	// name names a new plan for the developer's first message.
	name func(ctx context.Context, message string) (string, error)

	mu     sync.Mutex
	planID string
	// parentID is the plan's parent's ID, once parentRead says Family has
	// read it; "" for a top-level plan.
	parentID   string
	parentRead bool
}

// NewStore is the store for plan id in factory, or for a plan Create makes
// when id is "".
func NewStore(api *circleci.Client, id string) *Store {
	return &Store{api: api, name: factory.NamePlan, planID: id}
}

// ID is the plan's ID in factory, or "" before Create.
func (s *Store) ID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.planID
}

// skeleton is the empty plan factory's plan_init writes. The plan tools edit
// a plan by its sections, so a plan created without them gives the agent
// nothing to write into.
func skeleton(title string) string {
	return "# " + title + "\n\n## 1. Outcome\n\n## 2. Design\n\n## 3. Open Questions\n\n## 4. Worked example\n\n## 5. Units\n"
}

func (s *Store) Create(ctx context.Context, message string) (string, error) {
	title := s.title(ctx, message)
	p, err := s.api.CreatePlan(ctx, title, skeleton(title))
	if err != nil {
		return "", fmt.Errorf("create the plan in factory: %w", err)
	}
	s.mu.Lock()
	s.planID = p.ID
	s.mu.Unlock()
	return title, nil
}

// title is the plan's title for message, as Claude names it or, when Claude
// gives no usable name, as fallbackTitle.
func (s *Store) title(ctx context.Context, message string) string {
	name, err := s.name(ctx, message)
	if err != nil {
		// A plan Claude cannot name is still made; the developer is not told.
		return fallbackTitle(message)
	}
	return cmp.Or(slug(name, maxNameWords), fallbackTitle(message))
}

// errNotCreated is returned by a write before Create has set an ID to
// address it with.
var errNotCreated = errors.New("the plan has not been created in factory yet")

// State is the plan's flow state; "" before the plan is created, or when it
// has no flow.
func (s *Store) State(ctx context.Context) (string, error) {
	id := s.ID()
	if id == "" {
		return "", nil
	}
	f, err := s.api.GetFlow(ctx, id)
	if isNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get the plan's state: %w", err)
	}
	return f.State, nil
}

// Flow is the plan's flow as factory has it now; empty before the plan is
// created or has a flow.
func (s *Store) Flow(ctx context.Context) (plan.Flow, error) {
	id := s.ID()
	if id == "" {
		return plan.Flow{}, nil
	}
	f, err := s.api.GetFlow(ctx, id)
	if isNotFound(err) {
		return plan.Flow{}, nil
	}
	if err != nil {
		return plan.Flow{}, fmt.Errorf("get the plan's flow: %w", err)
	}
	out := plan.Flow{
		State:      f.State,
		Events:     make([]string, len(f.ApplicableEvents)),
		PR:         f.PR,
		Deviations: f.Deviations,
		Ended:      f.Phase == circleci.PhaseEnded,
		Outcome:    f.Outcome,
		Claimed:    f.SessionID != "",
		Holder:     f.SessionID,
	}
	for i, e := range f.ApplicableEvents {
		out.Events[i] = string(e)
	}
	if f.Task != nil {
		out.Task, out.Unit = f.Task.Category, f.Task.Unit
	}
	if len(f.Next) > 0 {
		out.Next = make([]plan.Step, len(f.Next))
		for i, t := range f.Next {
			out.Next[i] = plan.Step{
				Actor:      plan.Actor(t.Actor),
				Event:      string(t.Event),
				Task:       t.TaskCategory,
				Action:     t.Action,
				Dispatched: t.IsDispatched,
			}
		}
	}
	return out, nil
}

// Family is the plan's parent, open sub-plans and blockers, each with its
// flow's title and state. The parent is read once, as it never changes.
func (s *Store) Family(ctx context.Context) (plan.Family, error) {
	id := s.ID()
	if id == "" {
		return plan.Family{}, nil
	}
	parent, err := s.parent(ctx, id)
	if err != nil {
		return plan.Family{}, err
	}
	plans, err := s.api.ListPlans(ctx)
	if err != nil {
		return plan.Family{}, fmt.Errorf("list the plan's sub-plans: %w", err)
	}
	flows, err := s.api.ListFlows(ctx)
	if err != nil {
		return plan.Family{}, fmt.Errorf("list the plans' flows: %w", err)
	}
	blockers, err := s.api.ListFlowBlockers(ctx, id)
	if err != nil {
		return plan.Family{}, fmt.Errorf("list the plan's blockers: %w", err)
	}

	byID := make(map[string]circleci.Flow, len(flows))
	for _, f := range flows {
		byID[f.ID] = f
	}
	// relative is plan pid, titled title when it has no flow to title it.
	relative := func(pid, title string) plan.Relative {
		r := plan.Relative{ID: pid, Title: title}
		if f, ok := byID[pid]; ok {
			r.Title, r.State = cmp.Or(f.Title, title), f.State
			r.Outcome = f.Outcome
		}
		return r
	}

	var out plan.Family
	if parent != "" {
		var title string
		for _, p := range plans {
			if p.ID == parent {
				title = p.Title
			}
		}
		r := relative(parent, title)
		out.Parent = &r
	}
	for _, p := range plans {
		if p.Parent == id {
			out.Children = append(out.Children, relative(p.ID, p.Title))
		}
	}
	for _, b := range blockers {
		out.Blockers = append(out.Blockers, relative(b.ID, b.Title))
	}
	return out, nil
}

// parent is plan id's parent's ID, "" for a top-level plan, read from
// factory the first time and kept after.
func (s *Store) parent(ctx context.Context, id string) (string, error) {
	s.mu.Lock()
	if s.parentRead {
		defer s.mu.Unlock()
		return s.parentID, nil
	}
	s.mu.Unlock()
	p, err := s.api.GetPlan(ctx, id)
	if err != nil {
		return "", fmt.Errorf("get the plan's parent: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.parentID, s.parentRead = p.Parent, true
	return s.parentID, nil
}

// ErrDesignApproved is returned by ApproveDesign for a plan whose flow offers
// to approve the plan but not its design: the design is approved already.
var ErrDesignApproved = errors.New("the plan's design is approved already: approve the plan with ctrl+s")

// AddressComments sends the plan's flow factory's address-comments event,
// which moves it out of a Human state at its next poll.
func (s *Store) AddressComments(ctx context.Context) error {
	id := s.ID()
	if id == "" {
		return errNotCreated
	}
	if err := s.api.CreateFlowEvent(ctx, id, circleci.AddressComments); err != nil {
		return fmt.Errorf("send the address-comments event: %w", err)
	}
	return nil
}

// AnswerOpenQuestions sends the plan's flow factory's answer-open-questions
// event, which factory offers in place of address-comments for a plan with
// open questions, and which moves it out of a Human state at its next poll.
func (s *Store) AnswerOpenQuestions(ctx context.Context) error {
	id := s.ID()
	if id == "" {
		return errNotCreated
	}
	if err := s.api.CreateFlowEvent(ctx, id, circleci.AnswerOpenQuestions); err != nil {
		return fmt.Errorf("send the answer-open-questions event: %w", err)
	}
	return nil
}

// ResolveConflicts sends the plan's flow factory's resolve-conflicts event,
// which hands a plan whose PR conflicts back to the agent to rebase.
func (s *Store) ResolveConflicts(ctx context.Context) error {
	id := s.ID()
	if id == "" {
		return errNotCreated
	}
	if err := s.api.CreateFlowEvent(ctx, id, circleci.ResolveConflicts); err != nil {
		return fmt.Errorf("send the resolve-conflicts event: %w", err)
	}
	return nil
}

// StartWork sends the plan's flow factory's prioritize event, which moves an
// Unprioritized plan on for factory to start work on it.
func (s *Store) StartWork(ctx context.Context) error {
	id := s.ID()
	if id == "" {
		return errNotCreated
	}
	if err := s.api.CreateFlowEvent(ctx, id, circleci.Prioritize); err != nil {
		return fmt.Errorf("send the prioritize event: %w", err)
	}
	return nil
}

// Abandon sends the plan's flow factory's abandon event, for reason, which
// ends the plan without its PR merging at factory's next poll.
func (s *Store) Abandon(ctx context.Context, reason string) error {
	id := s.ID()
	if id == "" {
		return errNotCreated
	}
	if err := s.api.AbandonFlow(ctx, id, circleci.AbandonReason(reason)); err != nil {
		return fmt.Errorf("send the abandon event: %w", err)
	}
	return nil
}

// Delete deletes the plan in factory, with its comments and its flow. The
// store keeps its ID.
func (s *Store) Delete(ctx context.Context) error {
	id := s.ID()
	if id == "" {
		return errNotCreated
	}
	if err := s.api.DeletePlan(ctx, id); err != nil {
		return fmt.Errorf("delete the plan: %w", err)
	}
	return nil
}

// ApproveDesign sends the plan's flow factory's approve-for-units event,
// which approves its design. A flow that does not list the event now is
// refused without sending it: with ErrDesignApproved when it lists
// approve-for-building instead, and with planapprove.ErrQuestionsOpen when
// it lists neither. A plan with no flow yet sends it.
func (s *Store) ApproveDesign(ctx context.Context) error {
	id := s.ID()
	if id == "" {
		return errNotCreated
	}
	f, err := s.Flow(ctx)
	if err != nil {
		return fmt.Errorf("get the flow's events: %w", err)
	}
	events := f.Events
	if events != nil && !slices.Contains(events, string(circleci.ApproveForUnits)) {
		if slices.Contains(events, string(circleci.ApproveForBuilding)) {
			return ErrDesignApproved
		}
		return planapprove.ErrQuestionsOpen
	}
	if err := s.api.CreateFlowEvent(ctx, id, circleci.ApproveForUnits); err != nil {
		return fmt.Errorf("send the approve-for-units event: %w", err)
	}
	return nil
}

func (s *Store) Text(ctx context.Context) (string, error) {
	id := s.ID()
	if id == "" {
		return "", nil
	}
	text, err := s.api.PlanText(ctx, id)
	if err != nil {
		return "", fmt.Errorf("get the plan's text: %w", err)
	}
	return text, nil
}

func (s *Store) SetText(ctx context.Context, text string) error {
	id := s.ID()
	if id == "" {
		return errNotCreated
	}
	if err := s.api.SetPlanText(ctx, id, text); err != nil {
		return fmt.Errorf("set the plan's text: %w", err)
	}
	return nil
}

func (s *Store) Comments(ctx context.Context) ([]plan.Comment, error) {
	id := s.ID()
	if id == "" {
		return nil, nil
	}
	cs, err := s.api.ListComments(ctx, id, false)
	if err != nil {
		return nil, fmt.Errorf("list the plan's comments: %w", err)
	}
	out := make([]plan.Comment, len(cs))
	for i, c := range cs {
		out[i] = comment(c)
	}
	return out, nil
}

func (s *Store) AddComment(ctx context.Context, quote, body string) (plan.Comment, error) {
	id := s.ID()
	if id == "" {
		return plan.Comment{}, errNotCreated
	}
	c, err := s.api.CreateComment(ctx, id, quote, body)
	if err != nil {
		return plan.Comment{}, fmt.Errorf("save the comment: %w", err)
	}
	return comment(*c), nil
}

func comment(c circleci.PlanComment) plan.Comment {
	return plan.Comment{ID: c.ID, Quote: c.QuotedText, Body: c.Body, Resolved: c.ResolvedAt != nil}
}

func (s *Store) EditComment(ctx context.Context, id, body string) error {
	if s.ID() == "" {
		return errNotCreated
	}
	if err := s.api.SetCommentBody(ctx, id, body); err != nil {
		return fmt.Errorf("save the comment: %w", err)
	}
	return nil
}

func (s *Store) ResolveComment(ctx context.Context, id string) error {
	if s.ID() == "" {
		return errNotCreated
	}
	if _, err := s.api.ResolveComment(ctx, id); err != nil {
		return fmt.Errorf("resolve the comment: %w", err)
	}
	return nil
}

func (s *Store) DeleteComment(ctx context.Context, id string) error {
	if s.ID() == "" {
		return errNotCreated
	}
	if err := s.api.DeleteComment(ctx, id); err != nil {
		return fmt.Errorf("delete the comment: %w", err)
	}
	return nil
}

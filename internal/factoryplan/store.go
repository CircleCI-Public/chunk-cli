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
	"github.com/CircleCI-Public/chunk-cli/internal/planstate"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/plan"
)

// Store is a plan.Store backed by factory's V3 plan and comment API,
// addressed by the plan's ID, which Create sets.
type Store struct {
	api *circleci.Client
	// repo is the GitHub repository a plan Create makes records, "" for none.
	repo string
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
// when id is "". repo is the GitHub repository, as owner/name, a plan Create
// makes records; "" records none.
func NewStore(api *circleci.Client, id, repo string) *Store {
	return &Store{api: api, name: factory.NamePlan, planID: id, repo: repo}
}

// repos is repo as the repositories a new plan records: none for "".
func repos(repo string) []string {
	if repo == "" {
		return nil
	}
	return []string{repo}
}

// ID is the plan's ID in factory, or "" before Create.
func (s *Store) ID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.planID
}

// Create makes the plan in factory with no text, so factory writes its
// skeleton, recording the store's repository when it has one.
func (s *Store) Create(ctx context.Context, message string) (string, error) {
	title := s.title(ctx, message)
	p, err := s.api.CreatePlan(ctx, title, "", repos(s.repo)...)
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
// created or while it has no flow (see circleci.Client.GetFlow).
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
		State:        f.State,
		Events:       make([]string, len(f.ApplicableEvents)),
		PR:           f.PR,
		Deviations:   f.Deviations,
		Ended:        f.Phase == circleci.PhaseEnded,
		Outcome:      f.Outcome,
		Claimed:      f.Claimed(),
		Holder:       f.SessionID,
		Worker:       f.WorkerID,
		WaitingOn:    f.WaitingOn,
		WaitingOnWhy: f.WaitingOnWhy,
	}
	if p := f.Progress; p != nil {
		out.Progress = &plan.Progress{
			Summary: p.Summary, CurrentUnit: p.CurrentUnit,
			Findings: p.Findings, Fixups: p.Fixups,
			Deviations: p.Deviations, BindingDeviations: p.BindingDeviations,
		}
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
				Required:   t.IsRequired,
			}
		}
	}
	return out, nil
}

// Family is the plan's parent, open sub-plans and blockers, each with its
// flow's title and state; blockers are as the blockers endpoint serves them.
// The parent is read once, as it never changes.
func (s *Store) Family(ctx context.Context) (plan.Family, error) {
	id := s.ID()
	if id == "" {
		return plan.Family{}, nil
	}
	parent, err := s.parent(ctx, id)
	if err != nil {
		return plan.Family{}, err
	}
	flows, err := s.api.ListFlows(ctx)
	if err != nil {
		return plan.Family{}, fmt.Errorf("list the plans' flows: %w", err)
	}
	blockers, err := s.api.ListFlowBlockers(ctx, id)
	if err != nil {
		return plan.Family{}, fmt.Errorf("list the plan's blockers: %w", err)
	}

	relative := func(f circleci.Flow) plan.Relative {
		return plan.Relative{ID: f.ID, Title: f.Title, State: f.State, Outcome: f.Outcome}
	}
	var out plan.Family
	if parent != "" {
		r := plan.Relative{ID: parent}
		if i := slices.IndexFunc(flows, func(f circleci.Flow) bool { return f.ID == parent }); i >= 0 {
			r = relative(flows[i])
		}
		out.Parent = &r
	}
	for _, f := range flows {
		if f.Parent == id && f.Phase != circleci.PhaseEnded {
			out.Children = append(out.Children, relative(f))
		}
	}
	for _, b := range blockers {
		out.Blockers = append(out.Blockers, plan.Relative{ID: b.ID, Title: b.Title, State: b.State, Outcome: b.Outcome, Active: b.Active})
	}
	return out, nil
}

// Structure implements plan.Store.
func (s *Store) Structure(ctx context.Context) (plan.Structure, error) {
	id := s.ID()
	if id == "" {
		return plan.Structure{}, nil
	}
	sc, err := s.api.PlanStructuredContent(ctx, id)
	if err != nil {
		return plan.Structure{}, fmt.Errorf("get the plan's units: %w", err)
	}
	return StructureOf(sc), nil
}

// StructureOf is sc as the plan screen draws it: each section it holds, in plan
// order, then its units. Units is nil when sc has none.
func StructureOf(sc *circleci.PlanStructuredContent) plan.Structure {
	var out plan.Structure
	// 3. Open Questions has no blocks of its own: its questions' are its blocks.
	var questionBlocks []plan.Block
	if sc.OpenQuestions != nil {
		for _, q := range sc.OpenQuestions.Questions {
			out.OpenQuestions = append(out.OpenQuestions, plan.Question{Text: q.Text, Blocks: blocksOf(q.Blocks)})
			questionBlocks = append(questionBlocks, blocksOf(q.Blocks)...)
		}
	}
	for _, sec := range []struct {
		heading string
		section *circleci.PlanSection
	}{
		{"1. Outcome", sc.Outcome},
		{"2. Design", sc.Design},
		{"3. Open Questions", sc.OpenQuestions},
		{"4. Worked example", sc.WorkedExample},
	} {
		if sec.section == nil {
			continue
		}
		blocks := blocksOf(sec.section.Blocks)
		if sec.section == sc.OpenQuestions {
			blocks = questionBlocks
		}
		out.Sections = append(out.Sections, plan.Section{Heading: sec.heading, Blocks: blocks})
	}
	if sc.Units != nil {
		out.Units = make([]plan.Unit, len(sc.Units))
	}
	for i, u := range sc.Units {
		out.Units[i] = plan.Unit{Number: u.Number, Name: u.Name, Done: u.Done, Blocks: blocksOf(u.Blocks)}
	}
	return out
}

// blocksOf is bs as the plan screen's blocks.
func blocksOf(bs []circleci.PlanBlock) []plan.Block {
	if bs == nil {
		return nil
	}
	out := make([]plan.Block, len(bs))
	for i, b := range bs {
		out[i] = plan.Block{Type: b.Type, Text: b.Text, Blocks: blocksOf(b.Blocks)}
	}
	return out
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

// StartWork sends the plan's flow factory's prioritize event, which moves an
// init plan on for factory to start work on it.
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

// ErrNotEndable is End's error when the flow's next lists no event that
// ends the plan.
var ErrNotEndable = errors.New("factory offers no event that ends the plan")

// End sends the plan's flow the event its next lists to end the plan, end
// or abandon, for reason, which ends the plan without its PR merging at
// factory's next poll. It sends nothing, and fails with ErrNotEndable, when
// next lists neither.
func (s *Store) End(ctx context.Context, reason string) error {
	id := s.ID()
	if id == "" {
		return errNotCreated
	}
	f, err := s.Flow(ctx)
	if err != nil {
		return fmt.Errorf("get the flow's events: %w", err)
	}
	event := endEvent(f.Events)
	if event == "" {
		return ErrNotEndable
	}
	if err := s.api.EndFlow(ctx, id, event, circleci.AbandonReason(reason)); err != nil {
		return fmt.Errorf("send the %s event: %w", event, err)
	}
	return nil
}

// endEvent is the event among events that ends the plan, "" when none does.
func endEvent(events []string) circleci.FlowEvent {
	for _, e := range []circleci.FlowEvent{circleci.End, circleci.Abandon} {
		if slices.Contains(events, string(e)) {
			return e
		}
	}
	return ""
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
// which approves its design, when planstate.Approval offers it. Otherwise
// it is refused without sending it: with ErrDesignApproved when
// approve-for-building is offered instead, and with
// planapprove.ErrNoApproval when no approval is.
func (s *Store) ApproveDesign(ctx context.Context) error {
	id := s.ID()
	if id == "" {
		return errNotCreated
	}
	f, err := s.Flow(ctx)
	if err != nil {
		return fmt.Errorf("get the flow's events: %w", err)
	}
	next := make([]planstate.Move, len(f.Next))
	for i, step := range f.Next {
		next[i] = planstate.Move{Actor: string(step.Actor), Event: step.Event, Required: step.Required}
	}
	switch planstate.Approval(next) {
	case planstate.ApproveForBuilding:
		return ErrDesignApproved
	case "":
		return planapprove.ErrNoApproval
	case planstate.ApproveForUnits:
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

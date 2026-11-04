package plan

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sync"

	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
)

// fakeStore is an in-memory Store for tests, recording what is called.
type fakeStore struct {
	mu sync.Mutex

	Created bool
	Message string
	StateV  string
	// EventsV, once setEvents sets it, is the events the plan's flow
	// applies now; until then, while FlowV waits on the developer, they are
	// address-comments and both approvals.
	EventsV   []string
	eventsSet bool
	// RequiredV, once setRequired sets it, is the events of EventsV that
	// next marks required; until then, as Flow says.
	RequiredV   []string
	requiredSet bool
	// TaskV is the category of the task the plan's flow has out, as setTask
	// sets it; "" for none.
	TaskV string
	// FlowV is the Flow that Flow returns, with its State, Task and Events
	// filled from StateV, TaskV and EventsV.
	FlowV Flow
	// FamilyV is what Family returns.
	FamilyV Family
	// UnitsV, OpenQuestionsV and UnitsErr are what Structure returns in place of
	// what TextV holds, when set.
	UnitsV         []Unit
	OpenQuestionsV []Question
	TextV          string
	// TitleV is the title Create returns.
	TitleV string
	// CommentsV is the comments, ordered as added.
	CommentsV []Comment
	nextID    int

	// DesignApproved records the state the plan was in at each
	// ApproveDesign call, in order.
	DesignApproved []string
	// HandedOff records the state the plan was in at each AddressComments
	// call, in order. Like factory, which moves the plan at its next poll,
	// AddressComments leaves StateV as it is.
	HandedOff []string
	// Answered records the state the plan was in at each AnswerOpenQuestions
	// call, in order. Like AddressComments, it leaves StateV as it is.
	Answered []string
	// Started records the state the plan was in at each StartWork call, in
	// order.
	Started []string
	// Abandoned records the reasons Abandon was called with, in order.
	Abandoned []string
	// PlanDeleted is whether Delete deleted the plan.
	PlanDeleted bool
	// Added records the AddComment calls, in order.
	Added [][2]string // {quote, body}
	// Edited, Resolved and Deleted record the ids EditComment,
	// ResolveComment and DeleteComment were called with.
	Edited   map[string]string
	Resolved []string
	Deleted  []string

	// Set records the texts SetText was called with, in order.
	Set []string

	CreateErr, FlowErr, FamilyErr, UnitsErr, AddressErr, TextErr, CommentsErr error
	SetTextErr                                                                error
	AddErr, EditErr, ResolveErr, DeleteErr                                    error
	AbandonErr, DeletePlanErr                                                 error
	// FailAddAfter, when > 0, fails AddComment calls numbered after this
	// many successes.
	FailAddAfter int
	addCalls     int

	// OfferOnComment, when set, adds address-comments to the flow's events
	// once a comment has been added, as factory lists it only for a plan
	// with comments.
	OfferOnComment bool
	// TakeOnComment, when set, has the flow's next list a session
	// address-comments task once a comment has been added, as factory since
	// #324 hands a plan's comments off itself.
	TakeOnComment bool
	// TakeOnHandOff, when set, fails AddressComments and has the flow's
	// next list the session address-comments task from then on, as factory
	// would when its daemon dispatched the comments between the screen's
	// read and its send.
	TakeOnHandOff bool
	taken         bool
}

func newFakeStore() *fakeStore {
	return &fakeStore{StateV: "refine-proposal", FlowV: Flow{WaitingOn: string(ActorHuman)}, Edited: map[string]string{}}
}

func (f *fakeStore) ID() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Created {
		return "saved-plan"
	}
	return ""
}

func (f *fakeStore) Create(_ context.Context, message string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.CreateErr != nil {
		return "", f.CreateErr
	}
	f.Created, f.Message = true, message
	return f.TitleV, nil
}

func (f *fakeStore) Flow(context.Context) (Flow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.FlowErr != nil {
		return Flow{}, f.FlowErr
	}
	out := f.FlowV
	out.State, out.Task = f.StateV, f.TaskV
	switch {
	case f.eventsSet:
		out.Events = f.EventsV
	case f.FlowV.WaitingOn == string(ActorHuman):
		// Both approvals, as a flow factory stored before it told them
		// apart lists, so the plan's units decide which ctrl+s offers.
		out.Events = []string{"address-comments", "approve-for-units", "approve-for-building"}
	}
	if f.OfferOnComment && len(f.Added) > 0 && !slices.Contains(out.Events, "address-comments") {
		out.Events = append(slices.Clone(out.Events), "address-comments")
	}
	if out.Next == nil {
		// As conductor lists the events that apply in next, each marked
		// required as setRequired says, or as factory would: prioritize and
		// answer-open-questions always, and one approval, by the plan's
		// units: the design's with none, the plan's with any.
		required := f.RequiredV
		if !f.requiredSet {
			approval := "approve-for-units"
			if len(f.UnitsV) > 0 {
				approval = "approve-for-building"
			}
			required = []string{"prioritize", answerOpenQuestions, approval}
		}
		for _, e := range out.Events {
			out.Next = append(out.Next, Step{Actor: "human", Event: e, Required: slices.Contains(required, e)})
		}
	}
	if (f.TakeOnComment && len(f.Added) > 0) || f.taken {
		out.Next = append(slices.Clone(out.Next), Step{Actor: ActorSession, Task: "address-comments"})
	}
	return out, nil
}

func (f *fakeStore) Family(context.Context) (Family, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.FamilyV, f.FamilyErr
}

func (f *fakeStore) Structure(context.Context) (Structure, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := structureOfItems(fakes.PlanItems(f.TextV))
	if f.UnitsV != nil {
		parsed := st.Units
		st.Units = make([]Unit, len(f.UnitsV))
		for i, u := range f.UnitsV {
			st.Units[i] = Unit{Number: i + 1, Name: fakeUnitNumRe.ReplaceAllString(u.Name, ""), Done: u.Done}
			if i < len(parsed) {
				st.Units[i].Blocks = parsed[i].Blocks
			}
		}
	}
	if f.OpenQuestionsV != nil {
		st.OpenQuestions = f.OpenQuestionsV
	}
	return st, f.UnitsErr
}

// setTask has the plan's flow have a task of category out, or none for "".
func (f *fakeStore) setTask(category string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.TaskV = category
}

// setRequired has the plan's flow mark events, of those it applies, as the
// ones factory requires one of to move on, and no others.
func (f *fakeStore) setRequired(events ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.RequiredV, f.requiredSet = events, true
}

// setEvents has the plan's flow apply events now, whatever its state.
func (f *fakeStore) setEvents(events ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.EventsV, f.eventsSet = events, true
}

// setWaitingOn has the plan's flow name who as who factory waits on.
func (f *fakeStore) setWaitingOn(who string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.FlowV.WaitingOn = who
}

func (f *fakeStore) AddressComments(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.TakeOnHandOff {
		f.taken = true
		return errors.New("address-comments does not apply now")
	}
	if f.AddressErr != nil {
		return f.AddressErr
	}
	f.HandedOff = append(f.HandedOff, f.StateV)
	return nil
}

func (f *fakeStore) AnswerOpenQuestions(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.AddressErr != nil {
		return f.AddressErr
	}
	f.Answered = append(f.Answered, f.StateV)
	return nil
}

func (f *fakeStore) ApproveDesign(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.AddressErr != nil {
		return f.AddressErr
	}
	f.DesignApproved = append(f.DesignApproved, f.StateV)
	return nil
}

func (f *fakeStore) StartWork(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.AddressErr != nil {
		return f.AddressErr
	}
	f.Started = append(f.Started, f.StateV)
	return nil
}

func (f *fakeStore) Abandon(_ context.Context, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.AbandonErr != nil {
		return f.AbandonErr
	}
	f.Abandoned = append(f.Abandoned, reason)
	return nil
}

func (f *fakeStore) Delete(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.DeletePlanErr != nil {
		return f.DeletePlanErr
	}
	f.PlanDeleted = true
	return nil
}

func (f *fakeStore) Text(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.TextV, f.TextErr
}

func (f *fakeStore) SetText(_ context.Context, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.SetTextErr != nil {
		return f.SetTextErr
	}
	f.TextV = text
	f.Set = append(f.Set, text)
	return nil
}

func (f *fakeStore) Comments(context.Context) ([]Comment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.CommentsErr != nil {
		return nil, f.CommentsErr
	}
	out := make([]Comment, len(f.CommentsV))
	copy(out, f.CommentsV)
	return out, nil
}

func (f *fakeStore) AddComment(_ context.Context, quote, body string) (Comment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.addCalls++
	if f.AddErr != nil {
		return Comment{}, f.AddErr
	}
	if f.FailAddAfter > 0 && f.addCalls > f.FailAddAfter {
		return Comment{}, fmt.Errorf("add comment %d failed", f.addCalls)
	}
	f.Added = append(f.Added, [2]string{quote, body})
	f.nextID++
	c := Comment{ID: fmt.Sprintf("c%d", f.nextID), Quote: quote, Body: body}
	f.CommentsV = append(f.CommentsV, c)
	return c, nil
}

func (f *fakeStore) EditComment(_ context.Context, id, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.EditErr != nil {
		return f.EditErr
	}
	for i, c := range f.CommentsV {
		if c.ID == id {
			f.CommentsV[i].Body = body
		}
	}
	f.Edited[id] = body
	return nil
}

func (f *fakeStore) ResolveComment(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ResolveErr != nil {
		return f.ResolveErr
	}
	for i, c := range f.CommentsV {
		if c.ID == id {
			f.CommentsV[i].Resolved = true
		}
	}
	f.Resolved = append(f.Resolved, id)
	return nil
}

func (f *fakeStore) DeleteComment(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.DeleteErr != nil {
		return f.DeleteErr
	}
	kept := f.CommentsV[:0]
	for _, c := range f.CommentsV {
		if c.ID != id {
			kept = append(kept, c)
		}
	}
	f.CommentsV = kept
	f.Deleted = append(f.Deleted, id)
	return nil
}

// fakeUnitNumRe is the "Unit N:" the fake conductor drops from a unit's
// name, with the spaces after it.
var fakeUnitNumRe = regexp.MustCompile(`^Unit \d+: *`)

// structureOfItems is the Structure that factory's items hold: each section
// it has, in plan order, and its units, or none when it has no 5. Units.
func structureOfItems(items fakes.FactoryItems) Structure {
	var st Structure
	for _, sec := range []struct {
		heading string
		section *fakes.FactorySection
	}{
		{"1. Outcome", items.Outcome},
		{"2. Design", items.Design},
		{"4. Worked example", items.WorkedExample},
	} {
		if sec.section != nil {
			st.Sections = append(st.Sections, Section{Heading: sec.heading, Blocks: blocksOfItems(sec.section.Blocks)})
		}
	}
	if oq := items.OpenQuestions; oq != nil {
		// Its questions' blocks are its blocks: it has none of its own.
		sec := Section{Heading: "3. Open Questions"}
		for _, q := range oq.Questions {
			qn := Question{Text: q.Text, Blocks: blocksOfItems(q.Blocks)}
			st.OpenQuestions = append(st.OpenQuestions, qn)
			sec.Blocks = append(sec.Blocks, qn.Blocks...)
		}
		at := len(st.Sections)
		if items.WorkedExample != nil {
			at--
		}
		st.Sections = slices.Insert(st.Sections, at, sec)
	}
	if items.Units != nil {
		st.Units = make([]Unit, len(*items.Units))
		for i, u := range *items.Units {
			st.Units[i] = Unit{Number: u.Number, Name: u.Name, Done: u.IsDone, Blocks: blocksOfItems(u.Blocks)}
		}
	}
	return st
}

// blocksOfItems is bs as the plan screen's blocks.
func blocksOfItems(bs []fakes.FactoryBlock) []Block {
	out := make([]Block, len(bs))
	for i, b := range bs {
		out[i] = Block{Type: b.Type, Text: b.Text}
		if b.Blocks != nil {
			out[i].Blocks = blocksOfItems(*b.Blocks)
		}
	}
	return out
}

// setPlan has m draw text as factory's items hold it, as a refresh would.
func setPlan(m *Model, text string) {
	m.plan = text
	m.structure = structureOfItems(fakes.PlanItems(text))
}

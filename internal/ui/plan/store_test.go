package plan

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// fakeStore is an in-memory Store for tests, recording what is called.
type fakeStore struct {
	mu sync.Mutex

	Created bool
	Title   string
	StateV  string
	// EventsV, once setEvents sets it, is the events the plan's flow
	// applies now; until then they follow StateV, as factory's would for
	// address-comments and, in a Human state, both approvals.
	EventsV   []string
	eventsSet bool
	// TaskV is the category of the task the plan's flow has out, as setTask
	// sets it; "" for none.
	TaskV string
	TextV string
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
	// Added records the AddComment calls, in order.
	Added [][2]string // {quote, body}
	// Edited, Resolved and Deleted record the ids EditComment,
	// ResolveComment and DeleteComment were called with.
	Edited   map[string]string
	Resolved []string
	Deleted  []string

	CreateErr, StateErr, AddressErr, TextErr, CommentsErr error
	AddErr, EditErr, ResolveErr, DeleteErr                error
	// FailAddAfter, when > 0, fails AddComment calls numbered after this
	// many successes.
	FailAddAfter int
	addCalls     int
}

func newFakeStore() *fakeStore {
	return &fakeStore{StateV: "Planning - Human", Edited: map[string]string{}}
}

func (f *fakeStore) ID() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Created {
		return "saved-plan"
	}
	return ""
}

func (f *fakeStore) Create(_ context.Context, title string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.CreateErr != nil {
		return f.CreateErr
	}
	f.Created, f.Title = true, title
	return nil
}

func (f *fakeStore) State(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.StateV, f.StateErr
}

func (f *fakeStore) Flow(context.Context) (Flow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.eventsSet {
		return Flow{Events: f.EventsV, Task: f.TaskV}, nil
	}
	if strings.HasSuffix(f.StateV, " - Human") {
		// Both approvals, as a flow factory stored before it told them
		// apart lists, so the plan's units decide which ctrl+s offers.
		return Flow{Events: []string{"address-comments", "approve-for-units", "approve-for-building"}, Task: f.TaskV}, nil
	}
	return Flow{Task: f.TaskV}, nil
}

// setTask has the plan's flow have a task of category out, or none for "".
func (f *fakeStore) setTask(category string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.TaskV = category
}

// setEvents has the plan's flow apply events now, whatever its state.
func (f *fakeStore) setEvents(events ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.EventsV, f.eventsSet = events, true
}

func (f *fakeStore) AddressComments(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.AddressErr != nil {
		return f.AddressErr
	}
	f.HandedOff = append(f.HandedOff, f.StateV)
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

func (f *fakeStore) Text(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.TextV, f.TextErr
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

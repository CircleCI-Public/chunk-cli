// Package onboardingtest has fakes of onboarding's dependencies, for its
// tests and its host's.
package onboardingtest

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"sync"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/onboarding"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/plan"
)

// Fetched is one answer of Setup.Fetch.
type Fetched struct {
	Act   chunkd.SetupActivity
	Found bool
	Err   error
}

// Found is a Fetched of act.
func Found(act chunkd.SetupActivity) Fetched { return Fetched{Act: act, Found: true} }

// Setup is a scripted onboarding.Setup. Fetch answers its queue in turn,
// repeating the last answer; an empty queue answers found=false.
type Setup struct {
	mu        sync.Mutex
	queue     []Fetched
	start     chunkd.SetupActivity
	startErr  error
	onStart   []Fetched
	starts    []string
	continues int
	fetches   []int
	contErr   error
}

// NewSetup is a Setup whose Fetch answers fetches.
func NewSetup(fetches ...Fetched) *Setup { return &Setup{queue: fetches} }

// Then replaces the answers Fetch gives from now on.
func (s *Setup) Then(fetches ...Fetched) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queue = fetches
}

// OnStart has Start answer act and err, then Fetch answer then.
func (s *Setup) OnStart(act chunkd.SetupActivity, err error, then ...Fetched) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.start, s.startErr, s.onStart = act, err, then
}

// ContinueErr has Continue fail with err.
func (s *Setup) ContinueErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.contErr = err
}

func (s *Setup) Start(_ context.Context, orgID string) (chunkd.SetupActivity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.starts = append(s.starts, orgID)
	if s.onStart != nil {
		s.queue = s.onStart
	} else if s.startErr == nil {
		s.queue = []Fetched{Found(s.start)}
	}
	return s.start, s.startErr
}

func (s *Setup) Fetch(_ context.Context, after int) (chunkd.SetupActivity, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fetches = append(s.fetches, after)
	if len(s.queue) == 0 {
		return chunkd.SetupActivity{}, false, nil
	}
	f := s.queue[0]
	if len(s.queue) > 1 {
		s.queue = s.queue[1:]
	}
	return f.Act, f.Found, f.Err
}

func (s *Setup) Continue(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.continues++
	return s.contErr
}

// Starts is the org of each Start, in order.
func (s *Setup) Starts() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.starts)
}

// Continues is how many times Continue was called.
func (s *Setup) Continues() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.continues
}

// Fetches is the after of each Fetch, in order.
func (s *Setup) Fetches() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.fetches)
}

// Account is an onboarding.Account answering what Set last gave it.
type Account struct {
	mu    sync.Mutex
	org   string
	orgs  []onboarding.Org
	err   error
	calls int
}

// NewAccount is an Account answering org, orgs and err.
func NewAccount(org string, orgs []onboarding.Org, err error) *Account {
	return &Account{org: org, orgs: orgs, err: err}
}

// Set changes what Org answers.
func (a *Account) Set(org string, orgs []onboarding.Org, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.org, a.orgs, a.err = org, orgs, err
}

func (a *Account) Org(context.Context) (string, []onboarding.Org, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	return a.org, slices.Clone(a.orgs), a.err
}

// Calls is how many times Org was called.
func (a *Account) Calls() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}

// State is an in-memory onboarding.StateStore.
type State struct {
	mu    sync.Mutex
	s     onboarding.State
	saves int
}

// NewState is a State holding s, with the generic suggestions when s has
// none.
func NewState(s onboarding.State) *State {
	if s.Suggestions == nil {
		s.Suggestions = slices.Clone(onboarding.Generic)
	}
	return &State{s: s}
}

func (f *State) Load() onboarding.State {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.s
	s.Suggestions = slices.Clone(s.Suggestions)
	return s
}

func (f *State) Save(s onboarding.State) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	s.Suggestions = slices.Clone(s.Suggestions)
	f.s = s
	f.saves++
	return nil
}

// Saved is the state as last saved, or as given when never saved.
func (f *State) Saved() onboarding.State { return f.Load() }

// Agent is a plan.Agent recording its calls. Send delivers an event.
type Agent struct {
	mu        sync.Mutex
	requests  []string
	resumes   int
	resumeErr error
	events    chan plan.Event
}

// NewAgent is an Agent whose Resume fails with resumeErr.
func NewAgent(resumeErr error) *Agent {
	return &Agent{resumeErr: resumeErr, events: make(chan plan.Event, 64)}
}

func (a *Agent) Request(_ context.Context, text string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.requests = append(a.requests, text)
	return nil
}

func (a *Agent) Resume(context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.resumes++
	return a.resumeErr
}

func (a *Agent) Events() <-chan plan.Event { return a.events }

// Send delivers ev to the agent's reader.
func (a *Agent) Send(ev plan.Event) { a.events <- ev }

// Requests is each request's text, in order.
func (a *Agent) Requests() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.requests)
}

// Resumes is how many times Resume was called.
func (a *Agent) Resumes() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.resumes
}

// Store is a plan.Store recording its calls by name, in order. Create gives
// it NewID when it has no ID.
type Store struct {
	mu        sync.Mutex
	id        string
	newID     string
	created   []string
	flow      plan.Flow
	flowErr   error
	structure plan.Structure
	text      string
	comments  []plan.Comment
	calls     []string
	errs      map[string]error
}

// NewStore is a Store with id, "" for a plan not yet created, which Create
// names newID.
func NewStore(id, newID string) *Store {
	return &Store{id: id, newID: newID, errs: map[string]error{}}
}

// SetFlow changes what Flow answers.
func (s *Store) SetFlow(f plan.Flow, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flow, s.flowErr = f, err
}

// SetStructure changes what Structure answers.
func (s *Store) SetStructure(st plan.Structure) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.structure = st
}

// SetPlanText changes what Text answers, without recording a call.
func (s *Store) SetPlanText(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.text = text
}

// SetComments changes what Comments answers.
func (s *Store) SetComments(c []plan.Comment) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.comments = slices.Clone(c)
}

// Fail has the call named name fail with err.
func (s *Store) Fail(name string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.errs[name] = err
}

// Calls is the name of each recorded call, in order.
func (s *Store) Calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.calls)
}

// Created is each Create's message, in order.
func (s *Store) Created() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.created)
}

// Added is the comments added, in order.
func (s *Store) Added() []plan.Comment {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.comments)
}

func (s *Store) call(name string) error {
	s.calls = append(s.calls, name)
	return s.errs[name]
}

func (s *Store) ID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.id
}

func (s *Store) Create(_ context.Context, message string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.call("Create"); err != nil {
		return "", err
	}
	s.created = append(s.created, message)
	if s.id == "" {
		s.id = s.newID
	}
	return "first-change", nil
}

func (s *Store) Flow(context.Context) (plan.Flow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.flow, s.flowErr
}

func (s *Store) Family(context.Context) (plan.Family, error) { return plan.Family{}, nil }

func (s *Store) Structure(context.Context) (plan.Structure, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.structure, nil
}

func (s *Store) simple(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.call(name)
}

func (s *Store) AddressComments(context.Context) error { return s.simple("AddressComments") }

func (s *Store) AnswerOpenQuestions(context.Context) error {
	return s.simple("AnswerOpenQuestions")
}

func (s *Store) StartWork(context.Context) error       { return s.simple("StartWork") }
func (s *Store) Abandon(context.Context, string) error { return s.simple("Abandon") }
func (s *Store) Delete(context.Context) error          { return s.simple("Delete") }
func (s *Store) ApproveDesign(context.Context) error   { return s.simple("ApproveDesign") }
func (s *Store) SetText(context.Context, string) error { return s.simple("SetText") }
func (s *Store) EditComment(context.Context, string, string) error {
	return s.simple("EditComment")
}
func (s *Store) ResolveComment(context.Context, string) error { return s.simple("ResolveComment") }
func (s *Store) DeleteComment(context.Context, string) error  { return s.simple("DeleteComment") }

func (s *Store) Text(context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.text, nil
}

func (s *Store) Comments(context.Context) ([]plan.Comment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.comments), nil
}

func (s *Store) AddComment(_ context.Context, quote, body string) (plan.Comment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.call("AddComment"); err != nil {
		return plan.Comment{}, err
	}
	c := plan.Comment{ID: "c" + strconv.Itoa(len(s.comments)), Quote: quote, Body: body}
	s.comments = append(s.comments, c)
	return c, nil
}

// Opener opens Agent and Store for any plan, in Root, recording the IDs it
// is asked for; with Err set it fails.
type Opener struct {
	mu        sync.Mutex
	Agent     *Agent
	Store     *Store
	Root      string
	Err       error
	ids       []string
	approvals int
}

// NewOpener is an Opener of agent and store. Its Root is "", as for a plan
// with no known checkout, until set.
func NewOpener(agent *Agent, store *Store) *Opener { return &Opener{Agent: agent, Store: store} }

// Open is the onboarding.Opener.
func (o *Opener) Open(_ context.Context, _, id string) (onboarding.Opened, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.ids = append(o.ids, id)
	if o.Err != nil {
		return onboarding.Opened{}, o.Err
	}
	approve := func(func(string)) (string, error) {
		o.mu.Lock()
		defer o.mu.Unlock()
		o.approvals++
		return "build-1", nil
	}
	return onboarding.Opened{Agent: o.Agent, Approve: approve, Store: o.Store, Root: o.Root}, nil
}

// IDs is the ID of each Open, in order: "" for a new plan.
func (o *Opener) IDs() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Clone(o.ids)
}

// Approvals is how many times the approver was called.
func (o *Opener) Approvals() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.approvals
}

// ErrNoPlan is NoPlan's error.
var ErrNoPlan = errors.New("no plan")

// NoPlan is an onboarding.Opener that opens no plan.
func NoPlan(context.Context, string, string) (onboarding.Opened, error) {
	return onboarding.Opened{}, ErrNoPlan
}

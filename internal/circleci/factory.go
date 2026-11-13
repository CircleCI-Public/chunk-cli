package circleci

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"time"

	hc "github.com/CircleCI-Public/chunk-cli/internal/httpcl"
)

// Plan is a factory plan, as /api/v3/factory/plans serves it. Its text is read
// and replaced on its own, with PlanText and SetPlanText. Its phase and
// outcome are read from its flow, with GetFlow.
type Plan struct {
	ID     string
	Title  string
	Branch string
	// Repos are the GitHub repositories the plan is built in, as
	// OWNER/NAME; nil when it records none.
	Repos        []string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	OpenComments int
	// Parent is the ID of the plan this one is a sub-plan of, "" for a
	// top-level plan.
	Parent string
}

// Flow is a factory plan's progress through the code factory, as
// /api/v3/factory/flows serves it. Its ID is the plan's.
type Flow struct {
	ID string
	// Title is the flow's plan's title, "" from a conductor that does not
	// serve it.
	Title string
	// UpdatedAt is when the flow last changed, zero when not served.
	UpdatedAt time.Time
	State     string
	// Phase is PhaseReceived until the plan is prioritized, PhaseStarted
	// while its flow runs and PhaseEnded once it ends.
	Phase string
	// SessionID is the factory session holding the flow's claim: the one its
	// worker's sessions list it under; "" when none does, a headless session
	// counting as none. ListFlows reads no worker's sessions, so leaves it "".
	SessionID string
	// WorkerID is the factory worker claiming the flow, "" when none does or
	// it is not registered.
	WorkerID string
	// ApplicableEvents are the human events in Next, in order: what a
	// developer may send the flow now. nil when Next is.
	ApplicableEvents []FlowEvent
	// Task is the task factory has handed out for the plan and not yet had
	// back, or nil when none is out.
	Task *FlowTask
	// Outcome is how an ended flow ended, as factory names it; "" until then.
	Outcome string
	// PR is the flow's pull request's URL; "" while it has none.
	PR string
	// Deviations are what the flow's build is fixing now, as factory names
	// them.
	Deviations []string
	// Next is every way the flow can move on now, in order; empty when it
	// cannot move.
	Next []Transition
	// WaitingOn is who factory waits on to move the flow on, as factory
	// names them; "" from a conductor that does not serve it.
	WaitingOn string
	// WaitingOnWhy is why factory waits on WaitingOn; "" when not served.
	WaitingOnWhy string
	// Progress is how far the flow is along; nil once it ends or from an older
	// conductor.
	Progress *FlowProgress
	// Parent is the ID of the plan this flow's plan is a sub-plan of, "" for
	// a top-level plan.
	Parent string
}

// Claimed is whether a factory session or worker holds the flow's claim.
func (f Flow) Claimed() bool { return f.SessionID != "" || f.WorkerID != "" }

// FlowProgress is a flow's progress as factory serves it. A nil count does
// not apply.
type FlowProgress struct {
	Summary, CurrentUnit, DeviationSource, CurrentFixup                     string
	DoneUnits, Units, Deviations, BindingDeviations, SubPlans, OpenSubPlans *int
	Fixups, Findings, Revisions                                             int
}

// Transition is a way a flow can move on now, and who takes it. Actor is
// as factory names it, and factory sets one of Event, TaskCategory and
// Action. A transition is kept as served, even one chunk does not know.
type Transition struct {
	Actor        string
	Event        FlowEvent
	TaskCategory string
	// Action is what the human does outside factory, such as "merge-pr".
	Action string
	// IsDispatched is whether the task that takes it is the flow's
	// outstanding one.
	IsDispatched bool
	// IsRequired is whether it is one of the transitions one of which must
	// happen for the flow to move on.
	IsRequired bool
}

// FlowTask is a flow's outstanding task, as its factory_task reference
// carries it.
type FlowTask struct {
	ID string
	// Category is what kind of task it is, such as "early-plan".
	Category string
	// Unit is the heading of the unit a build-unit task builds, without
	// " (done)"; "" for other tasks, or from an older factory.
	Unit string
}

// Blocker is a plan blocking another, as its flow and the block stand now;
// State is "" for a blocker with no flow.
type Blocker struct {
	ID, Title, State string
	// Outcome is how the blocker's flow ended, "" until it ends.
	Outcome string
	// Active is whether it still holds the plan: is_active, true when the
	// response leaves it out.
	Active bool
}

// The phases a plan or its flow is in, as factory serves them.
const (
	PhaseReceived = "received"
	PhaseStarted  = "started"
	PhaseEnded    = "ended"
)

// FlowEvent is an event a human sends a plan's flow, which factory applies
// at the plan's next poll.
type FlowEvent string

// AddressComments moves a flow from a Human state to its phase's Agent
// state, where factory hands out a task for the plan's comments.
const AddressComments FlowEvent = "address-comments"

// AnswerOpenQuestions moves a flow from a Human state to its phase's Agent
// state, as AddressComments does, for a plan with open questions: factory
// offers it in place of AddressComments then.
const AnswerOpenQuestions FlowEvent = "answer-open-questions"

// ApproveForUnits moves a flow from Planning - Human to Planning - Agent with
// its design approved, where factory hands out a task to write its units.
const ApproveForUnits FlowEvent = "approve-for-units"

// ApproveForBuilding moves a flow from Planning - Human to Building - Agent,
// where factory hands the session claiming the plan its build tasks.
const ApproveForBuilding FlowEvent = "approve-for-building"

// Prioritize moves a flow from init to ready, where factory
// starts work on the plan. It applies at once when no task is out.
const Prioritize FlowEvent = "prioritize"

// End is the event that ends a flow without its PR merging; it takes an
// EndReason.
const End FlowEvent = "end"

// An EndReason is why a human ends a plan, as factory names it.
type EndReason string

// The reasons a plan is ended for. Factory ends the flow with outcome
// completed, canceled, rejected or duplicated.
const (
	EndCompleted EndReason = "completed"
	EndCanceled  EndReason = "canceled"
	EndWontDo    EndReason = "wont-do"
	EndDuplicate EndReason = "duplicate"
)

// PlanComment is a comment on a factory plan. QuotedText is the part of the
// plan's text it is about, or "" for a comment on the plan as a whole.
type PlanComment struct {
	ID         string
	PlanID     string
	QuotedText string
	Body       string
	CreatedAt  time.Time
	// ResolvedAt is nil while the comment is open.
	ResolvedAt *time.Time
}

type planAttrs struct {
	Title        string    `json:"title"`
	Branch       string    `json:"branch"`
	VCS          []vcsRepo `json:"vcs"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	OpenComments int       `json:"open_comment_count"`
}

func (a planAttrs) plan(id string) *Plan {
	p := &Plan{
		ID: id, Title: a.Title, Branch: a.Branch,
		CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt, OpenComments: a.OpenComments,
	}
	for _, v := range a.VCS {
		p.Repos = append(p.Repos, v.RepoFullName)
	}
	return p
}

type planRefs struct {
	Plans []planRef `json:"factory_plans"`
}

type planRef struct {
	ID  string `json:"id"`
	Rel string `json:"rel"`
}

// parent is the plan r names with rel parent, "" for none.
func (r planRefs) parent() string {
	for _, p := range r.Plans {
		if p.Rel == "parent" {
			return p.ID
		}
	}
	return ""
}

type flowAttrs struct {
	Title        string            `json:"title"`
	UpdatedAt    *time.Time        `json:"updated_at"`
	State        string            `json:"state"`
	Phase        string            `json:"phase"`
	Outcome      string            `json:"outcome"`
	PR           string            `json:"pr"`
	Deviations   []string          `json:"deviations"`
	Next         []transitionAttrs `json:"next"`
	WaitingOn    string            `json:"waiting_on"`
	WaitingOnWhy string            `json:"waiting_on_why"`
	Progress     *progressAttrs    `json:"progress"`
}

// progressAttrs is a flow's progress as factory serves it. Its fields are
// FlowProgress's, in order, so that one converts to the other.
type progressAttrs struct {
	Summary           string `json:"summary"`
	CurrentUnit       string `json:"current_unit"`
	DeviationSource   string `json:"deviation_source"`
	CurrentFixup      string `json:"current_fixup"`
	DoneUnits         *int   `json:"done_unit_count"`
	Units             *int   `json:"unit_count"`
	Deviations        *int   `json:"deviation_count"`
	BindingDeviations *int   `json:"binding_deviation_count"`
	SubPlans          *int   `json:"sub_plan_count"`
	OpenSubPlans      *int   `json:"open_sub_plan_count"`
	Fixups            int    `json:"fixup_count"`
	Findings          int    `json:"finding_count"`
	Revisions         int    `json:"revision_count"`
}

func (a flowAttrs) flow(id string, refs flowRefs) *Flow {
	f := &Flow{
		ID: id, Title: a.Title, State: a.State, Phase: a.Phase,
		ApplicableEvents: humanEvents(a.Next),
		Outcome:          a.Outcome, PR: a.PR, Deviations: a.Deviations,
		Next: transitions(a.Next), WaitingOn: a.WaitingOn, WaitingOnWhy: a.WaitingOnWhy,
		Parent: planRefs{Plans: refs.Plans}.parent(),
	}
	if a.Progress != nil {
		p := FlowProgress(*a.Progress)
		f.Progress = &p
	}
	if a.UpdatedAt != nil {
		f.UpdatedAt = *a.UpdatedAt
	}
	if refs.FactoryWorker != nil {
		f.WorkerID = refs.FactoryWorker.ID
	}
	if refs.FactoryTask != nil {
		f.Task = &FlowTask{
			ID: refs.FactoryTask.ID, Category: refs.FactoryTask.Attributes.Category,
			Unit: refs.FactoryTask.Attributes.Unit,
		}
	}
	return f
}

// transitionAttrs is one entry of a flow's next: a way it can move on now,
// and who takes it.
type transitionAttrs struct {
	Actor        string    `json:"actor"`
	Event        FlowEvent `json:"event"`
	TaskCategory string    `json:"task_category"`
	Action       string    `json:"action"`
	IsDispatched bool      `json:"is_dispatched"`
	IsRequired   bool      `json:"is_required"`
}

// transitions is next as served, every entry kept in order: nil when next
// is, as from a conductor that serves none, else non-nil.
func transitions(next []transitionAttrs) []Transition {
	if next == nil {
		return nil
	}
	out := make([]Transition, 0, len(next))
	for _, t := range next {
		out = append(out, Transition(t))
	}
	return out
}

// humanEvents is next's human events, in order: nil when next is, as from a
// conductor that serves none, else non-nil.
func humanEvents(next []transitionAttrs) []FlowEvent {
	if next == nil {
		return nil
	}
	out := []FlowEvent{}
	for _, t := range next {
		if t.Actor == "human" && t.Event != "" {
			out = append(out, t.Event)
		}
	}
	return out
}

type flowRefs struct {
	FactoryWorker *v3Ref    `json:"factory_worker"`
	FactoryTask   *taskRef  `json:"factory_task"`
	Plans         []planRef `json:"factory_plans"`
}

type taskRef struct {
	ID         string `json:"id"`
	Attributes struct {
		Category string `json:"category"`
		Unit     string `json:"unit"`
	} `json:"attributes"`
}

type eventAttrs struct {
	Event  FlowEvent `json:"event"`
	Reason EndReason `json:"reason,omitempty"`
}

type eventRefs struct {
	FactoryFlow v3Ref `json:"factory_flow"`
}

type createPlanAttrs struct {
	Title string    `json:"title"`
	Text  string    `json:"text,omitempty"`
	VCS   []vcsRepo `json:"vcs,omitempty"`
}

// vcsRepo is a repository a plan records, as conductor takes it.
type vcsRepo struct {
	Provider     string `json:"provider"`
	RepoFullName string `json:"repo_full_name"`
}

type planTextAttrs struct {
	Text string `json:"text"`
}

type commentBodyAttrs struct {
	Body string `json:"body"`
}

type commentAttrs struct {
	QuotedText string     `json:"quoted_text"`
	Body       string     `json:"body"`
	CreatedAt  time.Time  `json:"created_at"`
	ResolvedAt *time.Time `json:"resolved_at"`
}

type createCommentAttrs struct {
	QuotedText string `json:"quoted_text"`
	Body       string `json:"body"`
}

type factoryPlanRefs struct {
	FactoryPlan v3Ref `json:"factory_plan"`
}

// v3Page is a V3 list's paging block. Next is the cursor of the following
// page, nil on the last one.
type v3Page struct {
	Next *string `json:"next"`
}

// maxPages caps how many pages a list reads, so a server that never ends its
// list cannot hold a caller forever.
const maxPages = 1000

// headlessSession is the kind of a session factory runs with no developer;
// it holds no claim chunk reads as a session's.
const headlessSession = "headless"

// nextCursor is the cursor of the page after one that was read with cursor, or
// "" when that was the last. It is an error when the server points back at the
// cursor it was just asked for, or when pages reads have not reached the end.
func nextCursor(cursor string, p v3Page, pages int) (string, error) {
	if p.Next == nil || *p.Next == "" {
		return "", nil
	}
	if *p.Next == cursor {
		return "", fmt.Errorf("list does not advance: page cursor %q came back as the next one", cursor)
	}
	if pages >= maxPages {
		return "", fmt.Errorf("list is longer than %d pages", maxPages)
	}
	return *p.Next, nil
}

// CreatePlan creates a factory plan from a title and its first text, recording
// repos, each a GitHub repository as owner/name. For empty text factory writes
// the plan's skeleton itself.
func (c *Client) CreatePlan(ctx context.Context, title, text string, repos ...string) (*Plan, error) {
	body := createPlanAttrs{Title: title, Text: text}
	for _, r := range repos {
		body.VCS = append(body.VCS, vcsRepo{Provider: "github", RepoFullName: r})
	}
	var attrs planAttrs
	env := v3Envelope{Data: v3DataEntity{Attributes: &attrs}}
	_, err := c.cl.Call(ctx, hc.NewRequest(http.MethodPost, "/api/v3/factory/plans",
		hc.Body(v3Envelope{Data: v3DataEntity{Attributes: body}}),
		hc.JSONDecoder(&env),
	))
	if err != nil {
		return nil, mapErr("create plan", err)
	}
	return attrs.plan(env.Data.ID), nil
}

// CheckRun is a finished check run, as /api/v3/factory/check-runs serves it.
type CheckRun struct {
	ID   string
	Key  string
	Name string
	// Outcome is one of succeeded, failed, cancelled, skipped and timed_out.
	Outcome   string
	StartedAt time.Time
	EndedAt   time.Time
}

// checkRunAttrs is a check run's attributes on the wire.
type checkRunAttrs struct {
	Key       string    `json:"key"`
	Name      string    `json:"name"`
	Outcome   string    `json:"outcome"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at"`
}

// CreateCheckRun records a finished check run for the caller and returns it
// with its ID. It is not idempotent: each call stores a run. Both times are
// sent in RFC 3339, UTC, with sub-second precision kept so a short check's
// start still precedes its end.
func (c *Client) CreateCheckRun(ctx context.Context, r CheckRun) (*CheckRun, error) {
	body := checkRunAttrs{
		Key: r.Key, Name: r.Name, Outcome: r.Outcome,
		StartedAt: r.StartedAt.UTC(), EndedAt: r.EndedAt.UTC(),
	}
	var attrs checkRunAttrs
	env := v3Envelope{Data: v3DataEntity{Attributes: &attrs}}
	_, err := c.cl.Call(ctx, hc.NewRequest(http.MethodPost, "/api/v3/factory/check-runs",
		hc.Body(v3Envelope{Data: v3DataEntity{Attributes: body}}),
		hc.JSONDecoder(&env),
	))
	if err != nil {
		return nil, mapErr("create check run", err)
	}
	return &CheckRun{
		ID: env.Data.ID, Key: attrs.Key, Name: attrs.Name, Outcome: attrs.Outcome,
		StartedAt: attrs.StartedAt, EndedAt: attrs.EndedAt,
	}, nil
}

// ListPlans returns the caller's open plans, oldest first, reading every
// page. A plan is open until its flow ends; ended plans are not listed.
func (c *Client) ListPlans(ctx context.Context) ([]Plan, error) {
	var out []Plan
	var cursor string
	for pages := 1; ; pages++ {
		var page struct {
			Data []struct {
				ID         string    `json:"id"`
				Attributes planAttrs `json:"attributes"`
				References planRefs  `json:"references"`
			} `json:"data"`
			Page v3Page `json:"page"`
		}
		opts := []func(*hc.Request){
			hc.QueryParam("filter[user_id]", "me"),
			hc.QueryParam("filter[is_ended]", "false"),
			hc.JSONDecoder(&page),
		}
		if cursor != "" {
			opts = append(opts, hc.QueryParam("page[cursor]", cursor))
		}
		_, err := c.cl.Call(ctx, hc.NewRequest(http.MethodGet, "/api/v3/factory/plans", opts...))
		if err != nil {
			return nil, mapErr("list plans", err)
		}
		for _, item := range page.Data {
			p := item.Attributes.plan(item.ID)
			p.Parent = item.References.parent()
			out = append(out, *p)
		}
		next, err := nextCursor(cursor, page.Page, pages)
		if err != nil {
			return nil, err
		}
		if next == "" {
			return out, nil
		}
		cursor = next
	}
}

// GetPlan returns factory plan id.
func (c *Client) GetPlan(ctx context.Context, id string) (*Plan, error) {
	var attrs planAttrs
	var refs planRefs
	env := v3Envelope{Data: v3DataEntity{Attributes: &attrs, References: &refs}}
	_, err := c.cl.Call(ctx, hc.NewRequest(http.MethodGet, "/api/v3/factory/plans/%s",
		hc.RouteParams(url.PathEscape(id)),
		hc.JSONDecoder(&env),
	))
	if err != nil {
		return nil, mapErr("get plan", err)
	}
	p := attrs.plan(env.Data.ID)
	p.Parent = refs.parent()
	return p, nil
}

// DeletePlan deletes factory plan id, with its comments and its flow. A plan
// with sub-plans, or one a session claims, is a conflict (409).
func (c *Client) DeletePlan(ctx context.Context, id string) error {
	_, err := c.cl.Call(ctx, hc.NewRequest(http.MethodDelete, "/api/v3/factory/plans/%s",
		hc.RouteParams(url.PathEscape(id)),
	))
	if err != nil {
		return mapErr("delete plan", err)
	}
	return nil
}

// PlanText returns factory plan id's text.
func (c *Client) PlanText(ctx context.Context, id string) (string, error) {
	var attrs planTextAttrs
	env := v3Envelope{Data: v3DataEntity{Attributes: &attrs}}
	_, err := c.cl.Call(ctx, hc.NewRequest(http.MethodGet, "/api/v3/factory/plans/%s/text",
		hc.RouteParams(url.PathEscape(id)),
		hc.JSONDecoder(&env),
	))
	if err != nil {
		return "", mapErr("get plan text", err)
	}
	return attrs.Text, nil
}

// PlanStructuredContent is a plan's sections and the units of its 5. Units,
// as factory parses its text and serves them under items.
type PlanStructuredContent struct {
	// Each section is nil when factory found none, or two.
	Outcome, Design, OpenQuestions, WorkedExample *PlanSection
	// Units is nil when the plan has no 5. Units, and empty when it has none in it.
	Units []PlanUnit
}

// PlanSection is one section: its body as written, and the blocks factory
// parses it into. Questions is only on 3. Open Questions, which has no blocks of
// its own: its list's items.
type PlanSection struct {
	Text      string         `json:"text"`
	Blocks    []PlanBlock    `json:"blocks"`
	Questions []PlanQuestion `json:"questions"`
}

// PlanQuestion is one item of 3. Open Questions: its text, and the blocks
// factory parses it into.
type PlanQuestion struct {
	Text   string      `json:"text"`
	Blocks []PlanBlock `json:"blocks"`
}

// PlanBlock is one markdown block. Text is its source as written; Blocks is
// set only on a list, a list item or a blockquote.
type PlanBlock struct {
	Type   string      `json:"type"`
	Text   string      `json:"text"`
	Blocks []PlanBlock `json:"blocks"`
}

// PlanUnit is one unit of 5. Units. Number is factory's position of the unit,
// from 1. Name is factory's bare name: no "Unit N: " and no " (done)".
type PlanUnit struct {
	Number int
	Name   string
	Done   bool
	Text   string
	Blocks []PlanBlock
}

type planStructuredContentAttrs struct {
	Items struct {
		Outcome       *PlanSection `json:"outcome"`
		Design        *PlanSection `json:"design"`
		OpenQuestions *PlanSection `json:"open_questions"`
		WorkedExample *PlanSection `json:"worked_example"`
		Units         []struct {
			Number int         `json:"number"`
			Name   string      `json:"name"`
			IsDone bool        `json:"is_done"`
			Text   string      `json:"text"`
			Blocks []PlanBlock `json:"blocks"`
		} `json:"units"`
	} `json:"items"`
}

// PlanStructuredContent returns factory plan id's sections and units.
func (c *Client) PlanStructuredContent(ctx context.Context, id string) (*PlanStructuredContent, error) {
	var attrs planStructuredContentAttrs
	env := v3Envelope{Data: v3DataEntity{Attributes: &attrs}}
	_, err := c.cl.Call(ctx, hc.NewRequest(http.MethodGet, "/api/v3/factory/plans/%s/structured-content",
		hc.RouteParams(url.PathEscape(id)),
		hc.JSONDecoder(&env),
	))
	if err != nil {
		return nil, mapErr("get plan structured content", err)
	}
	items := attrs.Items
	out := &PlanStructuredContent{
		Outcome: items.Outcome, Design: items.Design,
		OpenQuestions: items.OpenQuestions, WorkedExample: items.WorkedExample,
	}
	if items.Units != nil {
		out.Units = make([]PlanUnit, 0, len(items.Units))
	}
	for _, u := range items.Units {
		out.Units = append(out.Units, PlanUnit{Number: u.Number, Name: u.Name, Done: u.IsDone, Text: u.Text, Blocks: u.Blocks})
	}
	return out, nil
}

// SetPlanText replaces factory plan id's text.
func (c *Client) SetPlanText(ctx context.Context, id, text string) error {
	_, err := c.cl.Call(ctx, hc.NewRequest(http.MethodPut, "/api/v3/factory/plans/%s/text",
		hc.RouteParams(url.PathEscape(id)),
		hc.Body(planTextAttrs{Text: text}),
	))
	if err != nil {
		return mapErr("set plan text", err)
	}
	return nil
}

// GetFlow returns factory plan planID's flow. A 404 is a plan with no flow:
// factory has not created it yet, or deleted it with its plan. An ended plan
// keeps its flow.
func (c *Client) GetFlow(ctx context.Context, planID string) (*Flow, error) {
	var attrs flowAttrs
	var refs flowRefs
	env := v3Envelope{Data: v3DataEntity{Attributes: &attrs, References: &refs}}
	_, err := c.cl.Call(ctx, hc.NewRequest(http.MethodGet, "/api/v3/factory/flows/%s",
		hc.RouteParams(url.PathEscape(planID)),
		hc.JSONDecoder(&env),
	))
	if err != nil {
		return nil, mapErr("get flow", err)
	}
	f := attrs.flow(env.Data.ID, refs)
	if f.WorkerID == "" {
		return f, nil
	}
	s, err := c.workerSessionFor(ctx, f.WorkerID, f.ID)
	if err != nil {
		return nil, err
	}
	if s != "" {
		f.SessionID = s
	}
	return f, nil
}

// workerSessionFor names the non-headless session worker workerID lists
// as working planID, "" when none does.
func (c *Client) workerSessionFor(ctx context.Context, workerID, planID string) (string, error) {
	var body struct {
		Data struct {
			Attributes struct {
				Sessions []struct {
					ID         string `json:"id"`
					Attributes struct {
						Kind string `json:"kind"`
					} `json:"attributes"`
					References struct {
						Flows []v3Ref `json:"factory_flows"`
					} `json:"references"`
				} `json:"sessions"`
			} `json:"attributes"`
		} `json:"data"`
	}
	_, err := c.cl.Call(ctx, hc.NewRequest(http.MethodGet, "/api/v3/factory/workers/%s/sessions",
		hc.RouteParams(url.PathEscape(workerID)),
		hc.JSONDecoder(&body),
	))
	if err != nil {
		return "", mapErr("get worker sessions", err)
	}
	for _, s := range body.Data.Attributes.Sessions {
		if s.Attributes.Kind == headlessSession {
			continue
		}
		if slices.ContainsFunc(s.References.Flows, func(r v3Ref) bool { return r.ID == planID }) {
			return s.ID, nil
		}
	}
	return "", nil
}

// ListFlows returns the caller's flows, ended ones included.
func (c *Client) ListFlows(ctx context.Context) ([]Flow, error) {
	var page struct {
		Data []struct {
			ID         string    `json:"id"`
			Attributes flowAttrs `json:"attributes"`
			References flowRefs  `json:"references"`
		} `json:"data"`
	}
	_, err := c.cl.Call(ctx, hc.NewRequest(http.MethodGet, "/api/v3/factory/flows",
		hc.QueryParam("filter[user_id]", "me"),
		hc.JSONDecoder(&page),
	))
	if err != nil {
		return nil, mapErr("list flows", err)
	}
	out := make([]Flow, 0, len(page.Data))
	for _, item := range page.Data {
		out = append(out, *item.Attributes.flow(item.ID, item.References))
	}
	return out, nil
}

// ListFlowBlockers returns the plans blocking plan planID, in the order
// the API lists them.
// A plan that is not the caller's, or that does not exist, is a 404.
func (c *Client) ListFlowBlockers(ctx context.Context, planID string) ([]Blocker, error) {
	var page struct {
		Data []struct {
			ID         string `json:"id"`
			Attributes struct {
				Title    string `json:"title"`
				State    string `json:"state"`
				Outcome  string `json:"outcome"`
				IsActive *bool  `json:"is_active"`
			} `json:"attributes"`
		} `json:"data"`
	}
	_, err := c.cl.Call(ctx, hc.NewRequest(http.MethodGet, "/api/v3/factory/flows/%s/blockers",
		hc.RouteParams(url.PathEscape(planID)),
		hc.JSONDecoder(&page),
	))
	if err != nil {
		return nil, mapErr("list flow blockers", err)
	}
	out := make([]Blocker, 0, len(page.Data))
	for _, item := range page.Data {
		a := item.Attributes
		out = append(out, Blocker{
			ID: item.ID, Title: a.Title, State: a.State, Outcome: a.Outcome,
			Active: a.IsActive == nil || *a.IsActive,
		})
	}
	return out, nil
}

// CreateFlowEvent sends event to factory plan planID's flow. Factory applies
// it at the plan's next poll, so the plan has not moved yet when this
// returns. It is not idempotent: each call stores an event. A plan with no
// flow is a 404.
func (c *Client) CreateFlowEvent(ctx context.Context, planID string, event FlowEvent) error {
	return c.createEvent(ctx, planID, eventAttrs{Event: event}, "create flow event")
}

// EndFlow sends factory plan planID's flow the end event, for reason.
// Factory ends the flow at the plan's next poll. A plan with no flow is a 404.
func (c *Client) EndFlow(ctx context.Context, planID string, reason EndReason) error {
	return c.createEvent(ctx, planID, eventAttrs{Event: End, Reason: reason}, "end flow")
}

// createEvent posts an event with attrs to factory plan planID's flow; label
// names the call in its error.
func (c *Client) createEvent(ctx context.Context, planID string, attrs eventAttrs, label string) error {
	_, err := c.cl.Call(ctx, hc.NewRequest(http.MethodPost, "/api/v3/factory/events",
		hc.Body(v3Envelope{Data: v3DataEntity{
			Attributes: attrs,
			References: eventRefs{FactoryFlow: v3Ref{ID: planID}},
		}}),
	))
	if err != nil {
		return mapErr(label, err)
	}
	return nil
}

// ReleaseClaim frees whatever claim plan planID's flow has, a worker's or a
// session's. The plan keeps its flow. A flow already unclaimed (204) is no
// error.
func (c *Client) ReleaseClaim(ctx context.Context, planID string) error {
	_, err := c.cl.Call(ctx, hc.NewRequest(http.MethodPost, "/api/v3/factory/flows/%s/release",
		hc.RouteParams(url.PathEscape(planID)),
	))
	if err != nil {
		return mapErr("release claim", err)
	}
	return nil
}

// CreateComment adds a comment to factory plan planID. quotedText is the part
// of the plan's text the comment is about, or "" for the plan as a whole.
func (c *Client) CreateComment(ctx context.Context, planID, quotedText, body string) (*PlanComment, error) {
	var attrs commentAttrs
	var refs factoryPlanRefs
	env := v3Envelope{Data: v3DataEntity{Attributes: &attrs, References: &refs}}
	_, err := c.cl.Call(ctx, hc.NewRequest(http.MethodPost, "/api/v3/factory/comments",
		hc.Body(v3Envelope{Data: v3DataEntity{
			Attributes: createCommentAttrs{QuotedText: quotedText, Body: body},
			References: factoryPlanRefs{FactoryPlan: v3Ref{ID: planID}},
		}}),
		hc.JSONDecoder(&env),
	))
	if err != nil {
		return nil, mapErr("create comment", err)
	}
	return planComment(env.Data.ID, refs.FactoryPlan.ID, attrs), nil
}

// ListComments returns factory plan planID's comments, oldest first, reading
// every page. open, when true, keeps only the comments not yet resolved.
func (c *Client) ListComments(ctx context.Context, planID string, open bool) ([]PlanComment, error) {
	var out []PlanComment
	var cursor string
	for pages := 1; ; pages++ {
		var page struct {
			Data []struct {
				ID         string          `json:"id"`
				Attributes commentAttrs    `json:"attributes"`
				References factoryPlanRefs `json:"references"`
			} `json:"data"`
			Page v3Page `json:"page"`
		}
		opts := []func(*hc.Request){
			hc.QueryParam("filter[factory_plan_id]", planID),
			hc.JSONDecoder(&page),
		}
		if open {
			opts = append(opts, hc.QueryParam("filter[resolved]", "false"))
		}
		if cursor != "" {
			opts = append(opts, hc.QueryParam("page[cursor]", cursor))
		}
		_, err := c.cl.Call(ctx, hc.NewRequest(http.MethodGet, "/api/v3/factory/comments", opts...))
		if err != nil {
			return nil, mapErr("list comments", err)
		}
		for _, item := range page.Data {
			out = append(out, *planComment(item.ID, item.References.FactoryPlan.ID, item.Attributes))
		}
		next, err := nextCursor(cursor, page.Page, pages)
		if err != nil {
			return nil, err
		}
		if next == "" {
			return out, nil
		}
		cursor = next
	}
}

// ResolveComment resolves comment id and returns it. Resolving a resolved
// comment keeps the time it was first resolved.
func (c *Client) ResolveComment(ctx context.Context, id string) (*PlanComment, error) {
	var attrs commentAttrs
	var refs factoryPlanRefs
	env := v3Envelope{Data: v3DataEntity{Attributes: &attrs, References: &refs}}
	_, err := c.cl.Call(ctx, hc.NewRequest(http.MethodPost, "/api/v3/factory/comments/%s/resolve",
		hc.RouteParams(url.PathEscape(id)),
		hc.JSONDecoder(&env),
	))
	if err != nil {
		return nil, mapErr("resolve comment", err)
	}
	return planComment(env.Data.ID, refs.FactoryPlan.ID, attrs), nil
}

// SetCommentBody replaces comment id's body.
func (c *Client) SetCommentBody(ctx context.Context, id, body string) error {
	_, err := c.cl.Call(ctx, hc.NewRequest(http.MethodPut, "/api/v3/factory/comments/%s/body",
		hc.RouteParams(url.PathEscape(id)),
		hc.Body(commentBodyAttrs{Body: body}),
	))
	if err != nil {
		return mapErr("set comment body", err)
	}
	return nil
}

// DeleteComment deletes comment id, open or resolved.
func (c *Client) DeleteComment(ctx context.Context, id string) error {
	_, err := c.cl.Call(ctx, hc.NewRequest(http.MethodDelete, "/api/v3/factory/comments/%s",
		hc.RouteParams(url.PathEscape(id)),
	))
	if err != nil {
		return mapErr("delete comment", err)
	}
	return nil
}

func planComment(id, planID string, a commentAttrs) *PlanComment {
	return &PlanComment{
		ID:         id,
		PlanID:     planID,
		QuotedText: a.QuotedText,
		Body:       a.Body,
		CreatedAt:  a.CreatedAt,
		ResolvedAt: a.ResolvedAt,
	}
}

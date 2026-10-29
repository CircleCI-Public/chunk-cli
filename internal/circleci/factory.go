package circleci

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	hc "github.com/CircleCI-Public/chunk-cli/internal/httpcl"
)

// Plan is a factory plan, as /api/v3/factory/plans serves it. Its text is read
// and replaced on its own, with PlanText and SetPlanText. Its phase and
// outcome are read from its flow, with GetFlow.
type Plan struct {
	ID           string
	Title        string
	Branch       string
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
	// SessionID is the factory session claiming the plan, or "" when none
	// does.
	SessionID string
	// ApplicableEvents are the events factory applies to the flow now;
	// any other sent now is stored and ignored.
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

// Blocker is a plan blocking another, with its own flow's state; State is
// "" for a blocker with no flow.
type Blocker struct {
	ID, Title, State string
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

// ResolveConflicts moves a flow from Integrating - Human to Integrating -
// Agent while its PR conflicts with its base, for factory to rebase it.
// Factory offers it only then.
const ResolveConflicts FlowEvent = "resolve-conflicts"

// Prioritize moves a flow from Unprioritized to Agent Ready, where factory
// starts work on the plan. It applies at once when no task is out.
const Prioritize FlowEvent = "prioritize"

// Abandon is the event that ends a flow without its PR merging; it takes an
// AbandonReason.
const Abandon FlowEvent = "abandon"

// An AbandonReason is why a human abandons a plan, as factory names it.
type AbandonReason string

// The reasons a plan is abandoned for. Factory ends the flow in Canceled,
// Won't Do or Duplicate, with outcome canceled, rejected or duplicated.
const (
	AbandonCanceled  AbandonReason = "canceled"
	AbandonWontDo    AbandonReason = "wont-do"
	AbandonDuplicate AbandonReason = "duplicate"
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
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	OpenComments int       `json:"open_comment_count"`
}

func (a planAttrs) plan(id string) *Plan {
	return &Plan{
		ID: id, Title: a.Title, Branch: a.Branch,
		CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt, OpenComments: a.OpenComments,
	}
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
	Title      string            `json:"title"`
	UpdatedAt  *time.Time        `json:"updated_at"`
	State      string            `json:"state"`
	Phase      string            `json:"phase"`
	Outcome    string            `json:"outcome"`
	PR         string            `json:"pr"`
	Deviations []string          `json:"deviations"`
	Next       []transitionAttrs `json:"next"`
}

func (a flowAttrs) flow(id string, refs flowRefs) *Flow {
	f := &Flow{
		ID: id, Title: a.Title, State: a.State, Phase: a.Phase,
		ApplicableEvents: humanEvents(a.Next),
		Outcome:          a.Outcome, PR: a.PR, Deviations: a.Deviations,
		Next: transitions(a.Next),
	}
	if a.UpdatedAt != nil {
		f.UpdatedAt = *a.UpdatedAt
	}
	if refs.FactorySession != nil {
		f.SessionID = refs.FactorySession.ID
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
	FactorySession *v3Ref   `json:"factory_session"`
	FactoryTask    *taskRef `json:"factory_task"`
}

type taskRef struct {
	ID         string `json:"id"`
	Attributes struct {
		Category string `json:"category"`
		Unit     string `json:"unit"`
	} `json:"attributes"`
}

type eventAttrs struct {
	Event  FlowEvent     `json:"event"`
	Reason AbandonReason `json:"reason,omitempty"`
}

type eventRefs struct {
	FactoryFlow v3Ref `json:"factory_flow"`
}

// releaseRequest is the body of a release, which conductor takes bare rather
// than in a data envelope.
type releaseRequest struct {
	FlowID string `json:"flow_id"`
}

type createPlanAttrs struct {
	Title string `json:"title"`
	Text  string `json:"text,omitempty"`
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

// CreatePlan creates a factory plan from a title and its first text. The plan
// starts Unprioritized, on a branch the API names for the title.
func (c *Client) CreatePlan(ctx context.Context, title, text string) (*Plan, error) {
	var attrs planAttrs
	env := v3Envelope{Data: v3DataEntity{Attributes: &attrs}}
	_, err := c.cl.Call(ctx, hc.NewRequest(http.MethodPost, "/api/v3/factory/plans",
		hc.Body(v3Envelope{Data: v3DataEntity{Attributes: createPlanAttrs{Title: title, Text: text}}}),
		hc.JSONDecoder(&env),
	))
	if err != nil {
		return nil, mapErr("create plan", err)
	}
	return attrs.plan(env.Data.ID), nil
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

// GetFlow returns factory plan planID's flow. A plan made before conductor
// gave every plan a flow has none, a 404.
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
	return attrs.flow(env.Data.ID, refs), nil
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
				Title string `json:"title"`
				State string `json:"state"`
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
		out = append(out, Blocker{ID: item.ID, Title: item.Attributes.Title, State: item.Attributes.State})
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

// AbandonFlow sends factory plan planID's flow the abandon event, for reason.
// Factory ends the flow at the plan's next poll, in Canceled, Won't Do or
// Duplicate. A plan with no flow is a 404.
func (c *Client) AbandonFlow(ctx context.Context, planID string, reason AbandonReason) error {
	return c.createEvent(ctx, planID, eventAttrs{Event: Abandon, Reason: reason}, "abandon flow")
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

// ReleaseClaim has factory session sessionID release its claim on plan
// planID, as the session itself would. The plan keeps its flow, and waits
// for a session to claim it again.
func (c *Client) ReleaseClaim(ctx context.Context, sessionID, planID string) error {
	_, err := c.cl.Call(ctx, hc.NewRequest(http.MethodPost, "/api/v3/factory/sessions/%s/release",
		hc.RouteParams(url.PathEscape(sessionID)),
		hc.Body(releaseRequest{FlowID: planID}),
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

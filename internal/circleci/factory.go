package circleci

import (
	"context"
	"net/http"
	"net/url"
	"time"

	hc "github.com/CircleCI-Public/chunk-cli/internal/httpcl"
)

// Plan is a factory plan, as /api/v3/factory/plans serves it. Its text is read
// and replaced on its own, with PlanText and SetPlanText.
type Plan struct {
	ID    string
	Title string
	State string
	// Branch is "" in ListPlans, whose rows leave it out.
	Branch       string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	OpenComments int
	// Claim is the factory session holding the plan, or nil when none does.
	// ListPlans leaves it nil, since its rows leave it out.
	Claim *PlanClaim
}

// PlanClaim is a factory session's claim on a plan.
type PlanClaim struct {
	SessionID string
	Worktree  string
	RenewedAt time.Time
}

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
	Title        string      `json:"title"`
	State        string      `json:"state"`
	Branch       string      `json:"branch"`
	CreatedAt    time.Time   `json:"created_at"`
	UpdatedAt    time.Time   `json:"updated_at"`
	OpenComments int         `json:"open_comment_count"`
	Claim        *claimAttrs `json:"claim"`
}

type claimAttrs struct {
	SessionID string    `json:"session_id"`
	Worktree  string    `json:"worktree"`
	RenewedAt time.Time `json:"renewed_at"`
}

func (a planAttrs) plan(id string) *Plan {
	p := &Plan{
		ID: id, Title: a.Title, State: a.State, Branch: a.Branch,
		CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt, OpenComments: a.OpenComments,
	}
	if a.Claim != nil {
		p.Claim = &PlanClaim{SessionID: a.Claim.SessionID, Worktree: a.Claim.Worktree, RenewedAt: a.Claim.RenewedAt}
	}
	return p
}

type createPlanAttrs struct {
	Title string `json:"title"`
	Text  string `json:"text,omitempty"`
}

type planTextAttrs struct {
	Text string `json:"text"`
}

type moveBody struct {
	State string `json:"state"`
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
// page. A plan is open until it is Done, Canceled, Won't Do or Duplicate.
func (c *Client) ListPlans(ctx context.Context) ([]Plan, error) {
	var out []Plan
	var cursor string
	for {
		var page struct {
			Data []struct {
				ID         string    `json:"id"`
				Attributes planAttrs `json:"attributes"`
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
			out = append(out, *item.Attributes.plan(item.ID))
		}
		if page.Page.Next == nil || *page.Page.Next == "" {
			return out, nil
		}
		cursor = *page.Page.Next
	}
}

// GetPlan returns factory plan id.
func (c *Client) GetPlan(ctx context.Context, id string) (*Plan, error) {
	var attrs planAttrs
	env := v3Envelope{Data: v3DataEntity{Attributes: &attrs}}
	_, err := c.cl.Call(ctx, hc.NewRequest(http.MethodGet, "/api/v3/factory/plans/%s",
		hc.RouteParams(url.PathEscape(id)),
		hc.JSONDecoder(&env),
	))
	if err != nil {
		return nil, mapErr("get plan", err)
	}
	return attrs.plan(env.Data.ID), nil
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

// MovePlan moves factory plan id to state and returns it. A move that is not
// an edge of factory's plan state machine is refused with a 400.
func (c *Client) MovePlan(ctx context.Context, id, state string) (*Plan, error) {
	var attrs planAttrs
	env := v3Envelope{Data: v3DataEntity{Attributes: &attrs}}
	_, err := c.cl.Call(ctx, hc.NewRequest(http.MethodPost, "/api/v3/factory/plans/%s/move",
		hc.RouteParams(url.PathEscape(id)),
		hc.Body(moveBody{State: state}),
		hc.JSONDecoder(&env),
	))
	if err != nil {
		return nil, mapErr("move plan", err)
	}
	return attrs.plan(env.Data.ID), nil
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
	for {
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
		if page.Page.Next == nil || *page.Page.Next == "" {
			return out, nil
		}
		cursor = *page.Page.Next
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

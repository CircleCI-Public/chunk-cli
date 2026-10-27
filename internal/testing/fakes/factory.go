package fakes

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// factoryPlan is a plan the fake holds, with its comments in the order they
// were made, as conductor keeps them.
type factoryPlan struct {
	id, title, state, branch, text string
	createdAt, updatedAt           time.Time
	// flowed is whether a session has claimed the plan, which gives it the
	// flow conductor keeps from then on; session is the one claiming it now,
	// "" when none does.
	flowed   bool
	session  string
	events   []string
	comments []factoryComment
	// seq orders plans by creation, as created_at and id order conductor's.
	seq int
}

type factoryComment struct {
	id, quotedText, body string
	createdAt            time.Time
	resolvedAt           *time.Time
}

// registerFactory serves the factory plan, comment, flow and event routes of
// the V3 API.
func (f *FakeCircleCI) registerFactory(r *gin.Engine) {
	r.POST("/api/v3/factory/plans", f.handleCreatePlan)
	r.GET("/api/v3/factory/plans", f.handleListPlans)
	r.GET("/api/v3/factory/plans/:id", f.handleGetPlan)
	r.GET("/api/v3/factory/plans/:id/text", f.handleGetPlanText)
	r.PUT("/api/v3/factory/plans/:id/text", f.handlePutPlanText)
	r.POST("/api/v3/factory/comments", f.handleCreateComment)
	r.GET("/api/v3/factory/comments", f.handleListComments)
	r.PUT("/api/v3/factory/comments/:id/body", f.handlePutCommentBody)
	r.POST("/api/v3/factory/comments/:id/resolve", f.handleResolveComment)
	r.DELETE("/api/v3/factory/comments/:id", f.handleDeleteComment)
	r.GET("/api/v3/factory/flows/:id", f.handleGetFlow)
	r.POST("/api/v3/factory/events", f.handleCreateEvent)
	r.POST("/api/v3/factory/sessions/:id/release", f.handleReleaseClaim)
}

// factoryEvents are the events a human may send a flow (flow.EventName in
// circleci/factory).
var factoryEvents = []string{"prioritize", "address-comments", "approve-for-units", "approve-for-building", "abandon-integrate", "abandon"}

// factoryEnds are the states a plan ends in, which the plan list leaves out
// (conductor.Ends in circleci/factory).
var factoryEnds = []string{"Done", "Canceled", "Won't Do", "Duplicate"}

// FactoryPlan is a plan as the fake holds it, for a test to inspect.
type FactoryPlan struct {
	ID, Title, State, Text string
	Comments               int
	// Session is the factory session claiming the plan, "" when none does.
	Session string
	// Events are the events sent the plan's flow, in order. The fake
	// stores them without applying them: conductor applies them at the
	// plan's next poll, which a test stands in for with
	// SetFactoryPlanState.
	Events []string
}

// FactoryPlan returns plan id as the fake holds it, and false when there is
// no such plan.
func (f *FakeCircleCI) FactoryPlan(id string) (FactoryPlan, bool) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	p := f.factoryPlans[id]
	if p == nil {
		return FactoryPlan{}, false
	}
	return FactoryPlan{
		ID: p.id, Title: p.title, State: p.state, Text: p.text, Comments: len(p.comments),
		Session: p.session, Events: slices.Clone(p.events),
	}, true
}

// SetFactoryPlanState puts plan id in state, as factory's daemon moving it
// would.
func (f *FakeCircleCI) SetFactoryPlanState(id, state string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p := f.factoryPlans[id]; p != nil {
		p.state = state
		p.updatedAt = time.Now().UTC()
	}
}

// ClaimFactoryPlan has factory session sessionID claim plan id, as factory's
// daemon would, giving the plan its flow.
func (f *FakeCircleCI) ClaimFactoryPlan(id, sessionID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p := f.factoryPlans[id]; p != nil {
		p.flowed, p.session = true, sessionID
	}
}

// ReleaseFactoryPlan has the session claiming plan id release it, as
// factory's daemon would; the plan keeps its flow.
func (f *FakeCircleCI) ReleaseFactoryPlan(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p := f.factoryPlans[id]; p != nil {
		p.session = ""
	}
}

// factoryError writes a V3 error, as conductor does.
func factoryError(c *gin.Context, status int, msg string) {
	c.JSON(status, gin.H{"error": gin.H{"id": "trace-1", "title": msg}})
}

// factoryStart checks the token and FactoryStatusCode, answering for the
// handler when either rejects the request.
func (f *FakeCircleCI) factoryStart(c *gin.Context) bool {
	if !f.requireToken(c) {
		return false
	}
	f.mu.RLock()
	status := f.FactoryStatusCode
	f.mu.RUnlock()
	if status != 0 {
		factoryError(c, status, "API error")
		return false
	}
	return true
}

func planEntity(p *factoryPlan) gin.H {
	attrs := planListAttrs(p)
	attrs["branch"] = p.branch
	return gin.H{"id": p.id, "attributes": attrs}
}

// planListAttrs are a plan's attributes as the plan list serves them, which
// leaves out its branch.
func planListAttrs(p *factoryPlan) gin.H {
	open := 0
	for _, cm := range p.comments {
		if cm.resolvedAt == nil {
			open++
		}
	}
	return gin.H{
		"title": p.title, "state": p.state,
		"created_at": p.createdAt.Format(time.RFC3339Nano), "updated_at": p.updatedAt.Format(time.RFC3339Nano),
		"open_comment_count": open,
	}
}

func commentEntity(planID string, cm factoryComment) gin.H {
	return gin.H{
		"id": cm.id,
		"attributes": gin.H{
			"quoted_text": cm.quotedText, "body": cm.body,
			"created_at": cm.createdAt, "resolved_at": cm.resolvedAt,
		},
		"references": gin.H{"user": gin.H{"id": "user-1"}, "factory_plan": gin.H{"id": planID}},
	}
}

func (f *FakeCircleCI) handleCreatePlan(c *gin.Context) {
	if !f.factoryStart(c) {
		return
	}
	var body struct {
		Data struct {
			Attributes struct {
				Title string `json:"title"`
				Text  string `json:"text"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Data.Attributes.Title == "" {
		factoryError(c, http.StatusBadRequest, "a plan needs a title")
		return
	}
	id := uuid.NewString()
	now := time.Now().UTC()
	f.mu.Lock()
	p := &factoryPlan{
		id: id, title: body.Data.Attributes.Title, state: "Unprioritized",
		branch: "factory/" + id, text: body.Data.Attributes.Text,
		createdAt: now, updatedAt: now, seq: len(f.factoryPlans),
	}
	if f.factoryPlans == nil {
		f.factoryPlans = map[string]*factoryPlan{}
	}
	f.factoryPlans[id] = p
	ent := planEntity(p)
	f.mu.Unlock()
	c.JSON(http.StatusCreated, gin.H{"data": ent})
}

// handleListPlans pages through the caller's open plans, oldest first,
// FactoryPageSize at a time, as conductor does. The fake has one user, so
// filter[user_id]=me lists every plan and any other user's UUID none.
func (f *FakeCircleCI) handleListPlans(c *gin.Context) {
	if !f.factoryStart(c) {
		return
	}
	user := c.Query("filter[user_id]")
	if user == "" {
		factoryError(c, http.StatusBadRequest, "filter[user_id] is required")
		return
	}
	if user != "me" && uuid.Validate(user) != nil {
		factoryError(c, http.StatusBadRequest, "filter[user_id] is not a uuid or me")
		return
	}
	after, ok := pageCursor(c)
	if !ok {
		return
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	var open []*factoryPlan
	if user == "me" {
		for _, p := range f.factoryPlans {
			if !slices.Contains(factoryEnds, p.state) {
				open = append(open, p)
			}
		}
	}
	slices.SortFunc(open, func(a, b *factoryPlan) int { return a.seq - b.seq })
	end := min(after+f.pageSize(), len(open))
	items := []gin.H{}
	for _, p := range open[min(after, len(open)):end] {
		items = append(items, gin.H{"id": p.id, "attributes": planListAttrs(p)})
	}
	resp := gin.H{"data": items}
	if end < len(open) {
		resp["page"] = gin.H{"next": strconv.Itoa(end)}
	}
	c.JSON(http.StatusOK, resp)
}

// pageCursor returns how many items c's page[cursor] says were already
// listed, answering 400 for the handler when it is not one of the fake's.
func pageCursor(c *gin.Context) (int, bool) {
	v := c.Query("page[cursor]")
	if v == "" {
		return 0, true
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		factoryError(c, http.StatusBadRequest, "bad cursor")
		return 0, false
	}
	return n, true
}

// pageSize is FactoryPageSize, or conductor's 20 when it is 0. f.mu must be
// held.
func (f *FakeCircleCI) pageSize() int {
	if f.FactoryPageSize == 0 {
		return 20
	}
	return f.FactoryPageSize
}

// pathID returns c's :id, answering 400 for the handler when it is not a
// UUID, as conductor does.
func pathID(c *gin.Context) (string, bool) {
	id := c.Param("id")
	if uuid.Validate(id) != nil {
		factoryError(c, http.StatusBadRequest, "id is not a uuid")
		return "", false
	}
	return id, true
}

// plan returns the plan c's :id names, answering 400 for the handler when
// :id is not a UUID and 404 when there is no such plan. f.mu must be held.
func (f *FakeCircleCI) plan(c *gin.Context) *factoryPlan {
	id, ok := pathID(c)
	if !ok {
		return nil
	}
	p := f.factoryPlans[id]
	if p == nil {
		factoryError(c, http.StatusNotFound, "plan not found")
	}
	return p
}

func (f *FakeCircleCI) handleGetPlan(c *gin.Context) {
	if !f.factoryStart(c) {
		return
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	if p := f.plan(c); p != nil {
		c.JSON(http.StatusOK, gin.H{"data": planEntity(p)})
	}
}

func (f *FakeCircleCI) handleGetPlanText(c *gin.Context) {
	if !f.factoryStart(c) {
		return
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	if p := f.plan(c); p != nil {
		c.JSON(http.StatusOK, gin.H{"data": gin.H{"id": p.id, "attributes": gin.H{"text": p.text}}})
	}
}

func (f *FakeCircleCI) handlePutPlanText(c *gin.Context) {
	if !f.factoryStart(c) {
		return
	}
	var body struct {
		Text string `json:"text"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		factoryError(c, http.StatusBadRequest, "bad request")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if p := f.plan(c); p != nil {
		p.text = body.Text
		p.updatedAt = time.Now().UTC()
		c.JSON(http.StatusOK, gin.H{"data": gin.H{"id": p.id, "attributes": gin.H{"text": p.text}}})
	}
}

// handleGetFlow serves a plan's flow, which it has once a session has
// claimed it, as conductor does; the flow's ID is the plan's.
func (f *FakeCircleCI) handleGetFlow(c *gin.Context) {
	if !f.factoryStart(c) {
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	p := f.factoryPlans[id]
	if p == nil || !p.flowed {
		factoryError(c, http.StatusNotFound, "flow not found")
		return
	}
	refs := gin.H{"factory_plan": gin.H{"id": p.id}}
	if p.session != "" {
		refs["factory_session"] = gin.H{"id": p.session}
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{
		"id": p.id, "attributes": gin.H{"state": p.state}, "references": refs,
	}})
}

// handleReleaseClaim drops session :id's claim on the plan flow_id names, as
// conductor does; the plan keeps its flow. It answers 404 for a plan that
// session does not claim.
func (f *FakeCircleCI) handleReleaseClaim(c *gin.Context) {
	if !f.factoryStart(c) {
		return
	}
	var body struct {
		FlowID string `json:"flow_id"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.FlowID == "" {
		factoryError(c, http.StatusBadRequest, "a release needs a flow_id")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	p := f.factoryPlans[body.FlowID]
	if p == nil || !p.flowed || p.session != c.Param("id") {
		factoryError(c, http.StatusNotFound, "claim not found")
		return
	}
	p.session = ""
	c.Status(http.StatusNoContent)
}

// handleCreateEvent stores an event for a flow, as conductor does, answering
// 400 for an event conductor does not know or one with no factory_flow
// reference, and 404 for a plan with no flow.
func (f *FakeCircleCI) handleCreateEvent(c *gin.Context) {
	if !f.factoryStart(c) {
		return
	}
	var body struct {
		Data struct {
			Attributes struct {
				Event string `json:"event"`
			} `json:"attributes"`
			References struct {
				FactoryFlow *struct {
					ID string `json:"id"`
				} `json:"factory_flow"`
			} `json:"references"`
		} `json:"data"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || !slices.Contains(factoryEvents, body.Data.Attributes.Event) {
		factoryError(c, http.StatusBadRequest, "event is not one of "+strings.Join(factoryEvents, ", "))
		return
	}
	if body.Data.References.FactoryFlow == nil {
		factoryError(c, http.StatusBadRequest, "an event needs a factory_flow reference")
		return
	}
	flowID := body.Data.References.FactoryFlow.ID
	f.mu.Lock()
	defer f.mu.Unlock()
	p := f.factoryPlans[flowID]
	if p == nil || !p.flowed {
		factoryError(c, http.StatusNotFound, "flow not found")
		return
	}
	p.events = append(p.events, body.Data.Attributes.Event)
	c.JSON(http.StatusCreated, gin.H{"data": gin.H{
		"id":         uuid.NewString(),
		"attributes": gin.H{"event": body.Data.Attributes.Event, "created_at": time.Now().UTC()},
		"references": gin.H{"factory_flow": gin.H{"id": p.id}, "user": gin.H{"id": "user-1"}},
	}})
}

func (f *FakeCircleCI) handleCreateComment(c *gin.Context) {
	if !f.factoryStart(c) {
		return
	}
	var body struct {
		Data struct {
			Attributes struct {
				QuotedText string `json:"quoted_text"`
				Body       string `json:"body"`
			} `json:"attributes"`
			References struct {
				FactoryPlan *struct {
					ID string `json:"id"`
				} `json:"factory_plan"`
			} `json:"references"`
		} `json:"data"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Data.Attributes.Body == "" {
		factoryError(c, http.StatusBadRequest, "a comment needs a body")
		return
	}
	if body.Data.References.FactoryPlan == nil {
		factoryError(c, http.StatusBadRequest, "a comment needs a factory_plan reference")
		return
	}
	planID := body.Data.References.FactoryPlan.ID
	if uuid.Validate(planID) != nil {
		factoryError(c, http.StatusBadRequest, "factory_plan id is not a uuid")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	p := f.factoryPlans[planID]
	if p == nil {
		factoryError(c, http.StatusNotFound, "plan not found")
		return
	}
	cm := factoryComment{
		id:         uuid.NewString(),
		quotedText: body.Data.Attributes.QuotedText, body: body.Data.Attributes.Body,
		createdAt: time.Now().UTC(),
	}
	p.comments = append(p.comments, cm)
	c.JSON(http.StatusCreated, gin.H{"data": commentEntity(planID, cm)})
}

// handleListComments pages through a plan's comments FactoryPageSize at a
// time (default 20, as conductor's). Its cursor is the count of comments
// already listed; conductor's is opaque too, so clients cannot tell. Like
// conductor, it leaves out the page block on the last page.
func (f *FakeCircleCI) handleListComments(c *gin.Context) {
	if !f.factoryStart(c) {
		return
	}
	planID := c.Query("filter[factory_plan_id]")
	if planID == "" {
		factoryError(c, http.StatusBadRequest, "filter[factory_plan_id] is required")
		return
	}
	if uuid.Validate(planID) != nil {
		factoryError(c, http.StatusBadRequest, "filter[factory_plan_id] is not a uuid")
		return
	}
	var resolved *bool
	if v := c.Query("filter[resolved]"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			factoryError(c, http.StatusBadRequest, "filter[resolved] is not a bool")
			return
		}
		resolved = &b
	}
	after, ok := pageCursor(c)
	if !ok {
		return
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	p := f.factoryPlans[planID]
	if p == nil {
		factoryError(c, http.StatusNotFound, "plan not found")
		return
	}
	size := f.pageSize()
	var matching []factoryComment
	for _, cm := range p.comments {
		if resolved == nil || (cm.resolvedAt != nil) == *resolved {
			matching = append(matching, cm)
		}
	}
	items := []gin.H{}
	end := min(after+size, len(matching))
	for _, cm := range matching[min(after, len(matching)):end] {
		items = append(items, commentEntity(planID, cm))
	}
	resp := gin.H{"data": items}
	if end < len(matching) {
		resp["page"] = gin.H{"next": strconv.Itoa(end)}
	}
	c.JSON(http.StatusOK, resp)
}

// comment finds the comment c's :id names, answering 400 for the handler
// when :id is not a UUID and 404 when there is no such comment. f.mu must be
// held.
func (f *FakeCircleCI) comment(c *gin.Context) (*factoryPlan, int) {
	id, ok := pathID(c)
	if !ok {
		return nil, -1
	}
	for _, p := range f.factoryPlans {
		for i := range p.comments {
			if p.comments[i].id == id {
				return p, i
			}
		}
	}
	factoryError(c, http.StatusNotFound, "comment not found")
	return nil, -1
}

func (f *FakeCircleCI) handlePutCommentBody(c *gin.Context) {
	if !f.factoryStart(c) {
		return
	}
	var body struct {
		Body string `json:"body"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Body == "" {
		factoryError(c, http.StatusBadRequest, "a comment needs a body")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	p, i := f.comment(c)
	if p == nil {
		return
	}
	p.comments[i].body = body.Body
	c.JSON(http.StatusOK, gin.H{"data": gin.H{"id": p.comments[i].id, "attributes": gin.H{"body": body.Body}}})
}

func (f *FakeCircleCI) handleResolveComment(c *gin.Context) {
	if !f.factoryStart(c) {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	p, i := f.comment(c)
	if p == nil {
		return
	}
	cm := &p.comments[i]
	if cm.resolvedAt == nil {
		now := time.Now().UTC()
		cm.resolvedAt = &now
	}
	c.JSON(http.StatusOK, gin.H{"data": commentEntity(p.id, *cm)})
}

func (f *FakeCircleCI) handleDeleteComment(c *gin.Context) {
	if !f.factoryStart(c) {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	p, i := f.comment(c)
	if p == nil {
		return
	}
	p.comments = slices.Delete(p.comments, i, i+1)
	c.Status(http.StatusNoContent)
}

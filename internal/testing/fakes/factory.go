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
// were made, as conductor keeps them. Its state is its flow's.
type factoryPlan struct {
	id, title, state, branch, text string
	createdAt, updatedAt           time.Time
	// noFlow is whether DropFactoryFlow took the plan's flow away, as a plan
	// made before conductor gave every plan one has none; session is the one
	// claiming the plan now, "" when none does.
	noFlow  bool
	session string
	events  []string
	// applicable are the human events the flow serves in next, as a test
	// sets them with SetFactoryApplicableEvents.
	applicable []string
	// taskID and taskCategory are the task the flow has out, as a test sets
	// it with SetFactoryTask; "" when none is. taskIDOnly serves its
	// reference as an older factory did, with no attributes.
	taskID, taskCategory string
	taskIDOnly           bool
	comments             []factoryComment
	// parent is the plan this one is a sub-plan of, "" for none, as a test
	// sets it with SetFactoryParent.
	parent string
	// status is the flow's status fields, as a test sets them with
	// SetFactoryFlowStatus.
	status FactoryFlowStatus
	// blockedBy are the plans blocking this one, in the order blocked.
	blockedBy []string
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
	r.GET("/api/v3/factory/flows", f.handleListFlows)
	r.GET("/api/v3/factory/flows/:id", f.handleGetFlow)
	r.GET("/api/v3/factory/flows/:id/blockers", f.handleListFlowBlockers)
	r.POST("/api/v3/factory/events", f.handleCreateEvent)
	r.POST("/api/v3/factory/sessions/:id/release", f.handleReleaseClaim)
}

// factoryEvents are the events a human may send a flow (flow.EventName in
// circleci/factory).
var factoryEvents = []string{"prioritize", "address-comments", "resolve-conflicts", "approve-for-units", "approve-for-building", "abandon-integrate", "abandon"}

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

// SetFactoryApplicableEvents has plan id's flow serve events as the human
// events of its next, the ones factory applies now, as conductor would for
// its state.
func (f *FakeCircleCI) SetFactoryApplicableEvents(id string, events ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p := f.factoryPlans[id]; p != nil {
		p.applicable = slices.Clone(events)
	}
}

// SetFactoryTask has plan id's flow have a task of category out, as factory
// handing one out would, or none when category is "".
func (f *FakeCircleCI) SetFactoryTask(id, category string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := f.factoryPlans[id]
	if p == nil {
		return
	}
	p.taskID, p.taskCategory, p.taskIDOnly = "", "", false
	if category != "" {
		p.taskID, p.taskCategory = uuid.NewString(), category
	}
}

// SetFactoryTaskIDOnly has plan id's flow have a task out that it references
// by ID alone, as factory did before its flows carried the task's attributes.
func (f *FakeCircleCI) SetFactoryTaskIDOnly(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p := f.factoryPlans[id]; p != nil {
		p.taskID, p.taskCategory, p.taskIDOnly = uuid.NewString(), "", true
	}
}

// FactoryTransition is a transition as conductor serves it in a flow's next.
type FactoryTransition struct {
	Actor, Event, TaskCategory, Action string
	IsDispatched                       bool
}

// FactoryFlowStatus is the part of a flow a test sets beyond its state.
type FactoryFlowStatus struct {
	Outcome, PR string
	Deviations  []string
	// Next, when not nil, is served as the flow's next in place of the one
	// the fake makes from its task, state and applicable events.
	Next []FactoryTransition
	// TaskUnit is served on the flow's task reference, when it has one.
	TaskUnit string
}

// SetFactoryParent makes plan id a sub-plan of plan parent, or of none when
// parent is "", as factory's plan init --parent would.
func (f *FakeCircleCI) SetFactoryParent(id, parent string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p := f.factoryPlans[id]; p != nil {
		p.parent = parent
	}
}

// SetFactoryFlowStatus has plan id's flow serve s, as conductor would for
// where its flow is.
func (f *FakeCircleCI) SetFactoryFlowStatus(id string, s FactoryFlowStatus) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p := f.factoryPlans[id]; p != nil {
		s.Deviations = slices.Clone(s.Deviations)
		s.Next = slices.Clone(s.Next)
		p.status = s
	}
}

// BlockFactoryPlan has the plans by block plan id, after any already
// blocking it, as factory's plan block would.
func (f *FakeCircleCI) BlockFactoryPlan(id string, by ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p := f.factoryPlans[id]; p != nil {
		p.blockedBy = append(p.blockedBy, by...)
	}
}

// ClaimFactoryPlan has factory session sessionID claim plan id, as factory's
// daemon would.
func (f *FakeCircleCI) ClaimFactoryPlan(id, sessionID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p := f.factoryPlans[id]; p != nil {
		p.session = sessionID
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

// DropFactoryFlow takes plan id's flow away, as a plan made before conductor
// gave every plan a flow has none: GET /flows/:id answers 404 and GET /flows
// leaves it out.
func (f *FakeCircleCI) DropFactoryFlow(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p := f.factoryPlans[id]; p != nil {
		p.noFlow = true
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
	attrs["phase"] = factoryPhase(p.state)
	if o := factoryOutcome(p.state); o != "" {
		attrs["outcome"] = o
	}
	ent := gin.H{"id": p.id, "attributes": attrs}
	addPlanRefs(ent, p)
	return ent
}

// addPlanRefs adds plan p's parent to ent's references, as conductor serves
// it, when p has one.
func addPlanRefs(ent gin.H, p *factoryPlan) {
	if p.parent != "" {
		ent["references"] = gin.H{"factory_plans": []gin.H{{"id": p.parent, "rel": "parent"}}}
	}
}

// factoryPhase is the phase factory serves for a plan or flow in state.
func factoryPhase(state string) string {
	switch {
	case strings.EqualFold(state, "Unprioritized"):
		return "received"
	case slices.ContainsFunc(factoryEnds, func(s string) bool { return strings.EqualFold(s, state) }):
		return "ended"
	}
	return "started"
}

// factoryOutcomes are how a plan in each end state ended, as factory names it.
var factoryOutcomes = map[string]string{
	"Done": "merged", "Canceled": "canceled", "Won't Do": "rejected", "Duplicate": "duplicated",
}

// factoryOutcome is how a plan in state ended, "" before it ends.
func factoryOutcome(state string) string { return factoryOutcomes[state] }

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
		"title":      p.title,
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
		item := gin.H{"id": p.id, "attributes": planListAttrs(p)}
		addPlanRefs(item, p)
		items = append(items, item)
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

// handleGetFlow serves a plan's flow, which conductor makes with the plan; the
// flow's ID is the plan's.
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
	if p == nil || p.noFlow {
		factoryError(c, http.StatusNotFound, "flow not found")
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": flowEntity(p)})
}

// handleListFlows serves every plan's flow, ended ones included, oldest plan
// first and unpaged, as conductor does. The fake has one user, so
// filter[user_id]=me or none lists every flow and any other user's UUID none.
func (f *FakeCircleCI) handleListFlows(c *gin.Context) {
	if !f.factoryStart(c) {
		return
	}
	user := c.Query("filter[user_id]")
	if user != "" && user != "me" && uuid.Validate(user) != nil {
		factoryError(c, http.StatusBadRequest, "filter[user_id] is not a uuid or me")
		return
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	var plans []*factoryPlan
	if user == "" || user == "me" {
		for _, p := range f.factoryPlans {
			if !p.noFlow {
				plans = append(plans, p)
			}
		}
	}
	slices.SortFunc(plans, func(a, b *factoryPlan) int { return a.seq - b.seq })
	items := []gin.H{}
	for _, p := range plans {
		items = append(items, flowEntity(p))
	}
	c.JSON(http.StatusOK, gin.H{"data": items})
}

// flowEntity is plan p's flow as both flow routes serve it.
func flowEntity(p *factoryPlan) gin.H {
	refs := gin.H{"factory_plan": gin.H{"id": p.id}}
	if p.session != "" {
		refs["factory_session"] = gin.H{"id": p.session}
	}
	switch {
	case p.taskIDOnly:
		refs["factory_task"] = gin.H{"id": p.taskID}
	case p.taskCategory != "":
		// The reference carries the task's attributes; the fake serves only
		// its category.
		refs["factory_task"] = gin.H{"id": p.taskID, "attributes": gin.H{
			"category": p.taskCategory, "unit": p.status.TaskUnit,
		}}
	}
	attrs := gin.H{
		"title": p.title, "updated_at": p.updatedAt.Format(time.RFC3339Nano),
		"state": p.state, "phase": factoryPhase(p.state), "next": flowNext(p),
		"pr": p.status.PR, "deviations": p.status.Deviations,
	}
	if p.status.Deviations == nil {
		attrs["deviations"] = []string{}
	}
	if p.status.Outcome != "" {
		attrs["outcome"] = p.status.Outcome
	}
	return gin.H{"id": p.id, "attributes": attrs, "references": refs}
}

// flowNext is p's flow's next: the one a test set with SetFactoryFlowStatus,
// else one made from its task, state and applicable events.
func flowNext(p *factoryPlan) []gin.H {
	next := []gin.H{}
	if p.status.Next != nil {
		for _, t := range p.status.Next {
			next = append(next, transitionEntity(t))
		}
		return next
	}
	if p.taskCategory != "" {
		next = append(next, gin.H{"actor": "session", "task_category": p.taskCategory, "is_dispatched": true})
	}
	if strings.EqualFold(p.state, "Integrating - Human") {
		next = append(next, gin.H{"actor": "human", "action": "merge-pr"})
	}
	for _, e := range p.applicable {
		next = append(next, gin.H{"actor": "human", "event": e})
	}
	return next
}

// transitionEntity is t as conductor serves it in a flow's next.
func transitionEntity(t FactoryTransition) gin.H {
	ent := gin.H{"actor": t.Actor}
	switch {
	case t.Event != "":
		ent["event"] = t.Event
	case t.TaskCategory != "":
		ent["task_category"] = t.TaskCategory
	case t.Action != "":
		ent["action"] = t.Action
	}
	if t.IsDispatched {
		ent["is_dispatched"] = true
	}
	return ent
}

// handleListFlowBlockers serves the plans blocking plan :id, in the order
// blocked, each with its own flow's state and phase. A blocker with no flow
// is served with an empty state and no phase, as
// conductor serves it. It answers 404 for an unknown plan.
func (f *FakeCircleCI) handleListFlowBlockers(c *gin.Context) {
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
	if p == nil {
		factoryError(c, http.StatusNotFound, "plan not found")
		return
	}
	items := []gin.H{}
	for _, bid := range p.blockedBy {
		b := f.factoryPlans[bid]
		if b == nil {
			continue
		}
		attrs := gin.H{"title": b.title, "state": ""}
		if !b.noFlow {
			attrs["state"] = b.state
			attrs["phase"] = factoryPhase(b.state)
		}
		items = append(items, gin.H{"id": b.id, "attributes": attrs})
	}
	c.JSON(http.StatusOK, gin.H{"data": items})
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
	if p == nil || p.noFlow || p.session != c.Param("id") {
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
	if p == nil || p.noFlow {
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

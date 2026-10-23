package fakes

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// factoryPlan is a plan the fake holds, with its comments in the order they
// were made, as conductor keeps them.
type factoryPlan struct {
	id, title, state, branch, text string
	comments                       []factoryComment
}

type factoryComment struct {
	id, quotedText, body string
	createdAt            time.Time
	resolvedAt           *time.Time
}

// registerFactory serves the factory plan and comment routes of the V3 API.
func (f *FakeCircleCI) registerFactory(r *gin.Engine) {
	r.POST("/api/v3/factory/plans", f.handleCreatePlan)
	r.GET("/api/v3/factory/plans/:id", f.handleGetPlan)
	r.GET("/api/v3/factory/plans/:id/text", f.handleGetPlanText)
	r.PUT("/api/v3/factory/plans/:id/text", f.handlePutPlanText)
	r.POST("/api/v3/factory/comments", f.handleCreateComment)
	r.GET("/api/v3/factory/comments", f.handleListComments)
	r.POST("/api/v3/factory/comments/:id/resolve", f.handleResolveComment)
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
	return gin.H{"id": p.id, "attributes": gin.H{"title": p.title, "state": p.state, "branch": p.branch}}
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
	f.mu.Lock()
	p := &factoryPlan{
		id: id, title: body.Data.Attributes.Title, state: "Unprioritized",
		branch: "factory/" + id, text: body.Data.Attributes.Text,
	}
	if f.factoryPlans == nil {
		f.factoryPlans = map[string]*factoryPlan{}
	}
	f.factoryPlans[id] = p
	ent := planEntity(p)
	f.mu.Unlock()
	c.JSON(http.StatusCreated, gin.H{"data": ent})
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
		c.JSON(http.StatusOK, gin.H{"data": gin.H{"id": p.id, "attributes": gin.H{"text": p.text}}})
	}
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
	after := 0
	if v := c.Query("page[cursor]"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			factoryError(c, http.StatusBadRequest, "bad cursor")
			return
		}
		after = n
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	p := f.factoryPlans[planID]
	if p == nil {
		factoryError(c, http.StatusNotFound, "plan not found")
		return
	}
	size := f.FactoryPageSize
	if size == 0 {
		size = 20
	}
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

func (f *FakeCircleCI) handleResolveComment(c *gin.Context) {
	if !f.factoryStart(c) {
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.factoryPlans {
		for i := range p.comments {
			cm := &p.comments[i]
			if cm.id != id {
				continue
			}
			if cm.resolvedAt == nil {
				now := time.Now().UTC()
				cm.resolvedAt = &now
			}
			c.JSON(http.StatusOK, gin.H{"data": commentEntity(p.id, *cm)})
			return
		}
	}
	factoryError(c, http.StatusNotFound, "comment not found")
}

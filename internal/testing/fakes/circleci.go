package fakes

import (
	"encoding/json"
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"

	"github.com/CircleCI-Public/chunk-cli/internal/testing/recorder"
)

type Collaboration struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	VCSType string `json:"vcs-type"`
	Slug    string `json:"slug"`
}

type Project struct {
	VCSType  string `json:"vcs_type"`
	Username string `json:"username"`
	Reponame string `json:"reponame"`
}

type Sandbox struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	OrgID    string `json:"org_id"`
	Provider string `json:"provider,omitempty"`
	Image    string `json:"image,omitempty"`
}

type RunResponse struct {
	RunID      string `json:"runId,omitempty"`
	PipelineID string `json:"pipelineId,omitempty"`
}

type ExecResponse struct {
	CommandID string `json:"command_id"`
	PID       int    `json:"pid"`
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr"`
	ExitCode  int    `json:"exit_code"`
}

// FakeCircleCI serves canned responses for the CircleCI API.
type FakeCircleCI struct {
	http.Handler
	Recorder *recorder.RequestRecorder

	mu             sync.RWMutex
	Collaborations []Collaboration
	Projects       []Project
	Sandboxes      []Sandbox
	RunResponse    *RunResponse
	AddKeyURL      string
	ExecResponse   *ExecResponse
	RunStatusCode  int // override status code for trigger run endpoint

	// Per-endpoint status code overrides for testing error responses.
	ListStatusCode   int // override for GET /sandbox/instances
	CreateStatusCode int // override for POST /sandbox/instances
	ExecStatusCode   int // override for POST /sandbox/instances/:id/exec
	AddKeyStatusCode int // override for POST /sandbox/instances/:id/ssh/add-key
	ResetStatusCode  int // override for POST /sandbox/instances/:id/reset

	ResetRequests []string // IDs of sandboxes reset via handleResetSandbox
}

func NewFakeCircleCI() *FakeCircleCI {
	r, rec := newRouter()
	f := &FakeCircleCI{
		Handler:   r,
		Recorder:  rec,
		AddKeyURL: "sandbox-abc.example.com",
	}

	// Existing endpoints
	r.GET("/api/v2/me/collaborations", f.handleCollaborations)
	r.GET("/api/v1.1/projects", f.handleProjects)

	// Sandbox endpoints
	r.GET("/api/v2/sandbox/instances", f.handleListSandboxes)
	r.POST("/api/v2/sandbox/instances", f.handleCreateSandbox)
	r.POST("/api/v2/sandbox/instances/:id/ssh/add-key", f.handleAddSSHKey)
	r.POST("/api/v2/sandbox/instances/:id/exec", f.handleExec)
	r.POST("/api/v2/sandbox/instances/:id/reset", f.handleResetSandbox)

	// Task run endpoint
	r.POST("/api/v2/agents/org/:org_id/project/:project_id/runs", f.handleTriggerRun)

	return f
}

func (f *FakeCircleCI) requireToken(c *gin.Context) bool {
	token := c.GetHeader("Circle-Token")
	if token == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"message": "Unauthorized"})
		return false
	}
	return true
}

func (f *FakeCircleCI) handleCollaborations(c *gin.Context) {
	if !f.requireToken(c) {
		return
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	c.JSON(http.StatusOK, f.Collaborations)
}

func (f *FakeCircleCI) handleProjects(c *gin.Context) {
	if !f.requireToken(c) {
		return
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	c.JSON(http.StatusOK, f.Projects)
}

func (f *FakeCircleCI) handleListSandboxes(c *gin.Context) {
	if !f.requireToken(c) {
		return
	}
	f.mu.RLock()
	defer f.mu.RUnlock()

	if f.ListStatusCode != 0 {
		c.JSON(f.ListStatusCode, gin.H{"message": "API error"})
		return
	}

	orgID := c.Query("org_id")
	var filtered []Sandbox
	for _, s := range f.Sandboxes {
		if s.OrgID == orgID {
			filtered = append(filtered, s)
		}
	}
	if filtered == nil {
		filtered = []Sandbox{}
	}
	c.JSON(http.StatusOK, gin.H{"items": filtered})
}

func (f *FakeCircleCI) handleCreateSandbox(c *gin.Context) {
	if !f.requireToken(c) {
		return
	}

	f.mu.RLock()
	statusCode := f.CreateStatusCode
	f.mu.RUnlock()
	if statusCode != 0 {
		c.JSON(statusCode, gin.H{"message": "API error"})
		return
	}

	var body struct {
		OrgID    string `json:"org_id"`
		Name     string `json:"name"`
		Provider string `json:"provider,omitempty"`
		Image    string `json:"image,omitempty"`
	}
	if err := json.NewDecoder(c.Request.Body).Decode(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Bad request"})
		return
	}

	sandbox := Sandbox{
		ID:    "sandbox-new-123",
		Name:  body.Name,
		OrgID: body.OrgID,
		Image: body.Image,
	}

	f.mu.Lock()
	f.Sandboxes = append(f.Sandboxes, sandbox)
	f.mu.Unlock()

	c.JSON(http.StatusCreated, sandbox)
}

func (f *FakeCircleCI) handleAddSSHKey(c *gin.Context) {
	if !f.requireToken(c) {
		return
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	if f.AddKeyStatusCode != 0 {
		c.JSON(f.AddKeyStatusCode, gin.H{"message": "API error"})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"url": f.AddKeyURL})
}

func (f *FakeCircleCI) handleExec(c *gin.Context) {
	if !f.requireToken(c) {
		return
	}
	f.mu.RLock()
	resp := f.ExecResponse
	statusCode := f.ExecStatusCode
	f.mu.RUnlock()

	if statusCode != 0 {
		c.JSON(statusCode, gin.H{"message": "API error"})
		return
	}

	if resp != nil {
		c.JSON(http.StatusOK, resp)
		return
	}

	// Default response
	c.JSON(http.StatusOK, ExecResponse{
		CommandID: "cmd-123",
		PID:       42,
		Stdout:    "ok\n",
		Stderr:    "",
		ExitCode:  0,
	})
}

func (f *FakeCircleCI) handleTriggerRun(c *gin.Context) {
	if !f.requireToken(c) {
		return
	}
	f.mu.RLock()
	resp := f.RunResponse
	statusCode := f.RunStatusCode
	f.mu.RUnlock()

	if statusCode != 0 {
		c.JSON(statusCode, gin.H{"message": "API error"})
		return
	}

	if resp != nil {
		c.JSON(http.StatusOK, resp)
		return
	}

	c.JSON(http.StatusOK, RunResponse{
		RunID:      "run-abc-123",
		PipelineID: "pipeline-def-456",
	})
}

func (f *FakeCircleCI) handleResetSandbox(c *gin.Context) {
	if !f.requireToken(c) {
		return
	}

	f.mu.RLock()
	statusCode := f.ResetStatusCode
	f.mu.RUnlock()

	if statusCode != 0 {
		c.JSON(statusCode, gin.H{"message": "API error"})
		return
	}

	id := c.Param("id")

	f.mu.Lock()
	f.ResetRequests = append(f.ResetRequests, id)
	f.mu.Unlock()

	c.JSON(http.StatusOK, gin.H{"id": id})
}

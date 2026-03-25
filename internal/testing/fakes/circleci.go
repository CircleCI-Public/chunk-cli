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
	ID             string `json:"id"`
	Name           string `json:"name"`
	OrganizationID string `json:"organization_id"`
	Image          string `json:"image,omitempty"`
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

type Command struct {
	ID       string `json:"id"`
	PID      int    `json:"pid"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
	Status   string `json:"status"`
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
	CommandResponse *Command
	RunStatusCode  int // override status code for trigger run endpoint
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
	r.DELETE("/api/v2/sandbox/instances/:id", f.handleDeleteSandbox)
	r.POST("/api/v2/sandbox/instances/:id/ssh/add-key", f.handleAddSSHKey)
	r.POST("/api/v2/sandbox/instances/:id/exec", f.handleExec)
	r.GET("/api/v2/sandbox/commands/:id", f.handleGetCommand)

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

	orgID := c.Query("org_id")
	var filtered []Sandbox
	for _, s := range f.Sandboxes {
		if s.OrganizationID == orgID {
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

	var body struct {
		OrganizationID string `json:"organization_id"`
		Name           string `json:"name"`
		Image          string `json:"image,omitempty"`
	}
	if err := json.NewDecoder(c.Request.Body).Decode(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Bad request"})
		return
	}

	sandbox := Sandbox{
		ID:             "sandbox-new-123",
		Name:           body.Name,
		OrganizationID: body.OrganizationID,
		Image:          body.Image,
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
	c.JSON(http.StatusCreated, gin.H{"url": f.AddKeyURL})
}

func (f *FakeCircleCI) handleExec(c *gin.Context) {
	if !f.requireToken(c) {
		return
	}
	f.mu.RLock()
	resp := f.ExecResponse
	f.mu.RUnlock()

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

func (f *FakeCircleCI) handleDeleteSandbox(c *gin.Context) {
	if !f.requireToken(c) {
		return
	}
	id := c.Param("id")
	f.mu.Lock()
	defer f.mu.Unlock()
	filtered := f.Sandboxes[:0]
	for _, s := range f.Sandboxes {
		if s.ID != id {
			filtered = append(filtered, s)
		}
	}
	f.Sandboxes = filtered
	c.Status(http.StatusNoContent)
}

func (f *FakeCircleCI) handleGetCommand(c *gin.Context) {
	if !f.requireToken(c) {
		return
	}
	id := c.Param("id")
	f.mu.RLock()
	resp := f.CommandResponse
	f.mu.RUnlock()

	if resp != nil {
		c.JSON(http.StatusOK, resp)
		return
	}

	c.JSON(http.StatusOK, Command{
		ID:       id,
		PID:      42,
		Stdout:   "ok\n",
		Stderr:   "",
		ExitCode: 0,
		Status:   "completed",
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

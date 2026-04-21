package fakes

import (
	"encoding/json"
	"fmt"
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

type Snapshot struct {
	ID             string `json:"id"`
	OrganizationID string `json:"organization_id"`
	Name           string `json:"name"`
	Tag            string `json:"tag,omitempty"`
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

	mu              sync.RWMutex
	snapshotCounter int
	Collaborations  []Collaboration
	Projects        []Project
	Sandboxes       []Sandbox
	Snapshots       []Snapshot
	RunResponse     *RunResponse
	AddKeyURL       string
	ExecResponse    *ExecResponse
	RunStatusCode   int // override status code for trigger run endpoint

	// Per-endpoint status code overrides for testing error responses.
	CollaborationsStatusCode int // override for GET /me/collaborations
	ListStatusCode           int // override for GET /sandbox/instances
	CreateStatusCode         int // override for POST /sandbox/instances
	ExecStatusCode           int // override for POST /sandbox/instances/:id/exec
	AddKeyStatusCode         int // override for POST /sandbox/instances/:id/ssh/add-key
	CreateSnapshotStatusCode int // override for POST /sandbox/snapshots
	GetSnapshotStatusCode    int // override for GET /sandbox/snapshots/:id
}

func NewFakeCircleCI() *FakeCircleCI {
	r, rec := newRouter()
	f := &FakeCircleCI{
		Handler:   r,
		Recorder:  rec,
		AddKeyURL: "sandbox-abc.example.com",
	}

	// Existing endpoints
	r.GET("/api/v2/me", f.handleGetCurrentUser)
	r.GET("/api/v2/me/collaborations", f.handleCollaborations)
	r.GET("/api/v1.1/projects", f.handleProjects)

	// Sandbox endpoints
	r.GET("/api/v2/sandbox/instances", f.handleListSandboxes)
	r.POST("/api/v2/sandbox/instances", f.handleCreateSandbox)
	r.POST("/api/v2/sandbox/instances/:id/ssh/add-key", f.handleAddSSHKey)
	r.POST("/api/v2/sandbox/instances/:id/exec", f.handleExec)

	// Snapshot endpoints
	r.POST("/api/v2/sandbox/snapshots", f.handleCreateSnapshot)
	r.GET("/api/v2/sandbox/snapshots/:id", f.handleGetSnapshot)

	// Task run endpoint
	r.POST("/api/v2/agents/org/:org_id/project/:project_id/runs", f.handleTriggerRun)

	return f
}

func (f *FakeCircleCI) handleGetCurrentUser(c *gin.Context) {
	if !f.requireToken(c) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": "user-123", "login": "testuser"})
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
	if f.CollaborationsStatusCode != 0 {
		c.JSON(f.CollaborationsStatusCode, gin.H{"message": "API error"})
		return
	}
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

func (f *FakeCircleCI) handleCreateSnapshot(c *gin.Context) {
	if !f.requireToken(c) {
		return
	}
	f.mu.RLock()
	statusCode := f.CreateSnapshotStatusCode
	f.mu.RUnlock()
	if statusCode != 0 {
		c.JSON(statusCode, gin.H{"message": "API error"})
		return
	}

	var body struct {
		SandboxID string `json:"sandbox_id"`
		Name      string `json:"name"`
	}
	if err := json.NewDecoder(c.Request.Body).Decode(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Bad request"})
		return
	}

	f.mu.Lock()
	f.snapshotCounter++
	snap := Snapshot{
		ID:   fmt.Sprintf("snap-%d", f.snapshotCounter),
		Name: body.Name,
	}
	f.Snapshots = append(f.Snapshots, snap)
	f.mu.Unlock()

	c.JSON(http.StatusCreated, snap)
}

func (f *FakeCircleCI) handleGetSnapshot(c *gin.Context) {
	if !f.requireToken(c) {
		return
	}
	f.mu.RLock()
	defer f.mu.RUnlock()

	if f.GetSnapshotStatusCode != 0 {
		c.JSON(f.GetSnapshotStatusCode, gin.H{"message": "API error"})
		return
	}

	id := c.Param("id")
	for _, s := range f.Snapshots {
		if s.ID == id {
			c.JSON(http.StatusOK, s)
			return
		}
	}
	c.JSON(http.StatusNotFound, gin.H{"message": "snapshot not found"})
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

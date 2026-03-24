package fakes

import (
	"encoding/json"
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"

	"github.com/CircleCI-Public/chunk-cli/acceptance/testutil"
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

// FakeCircleCI serves canned responses for the CircleCI API.
type FakeCircleCI struct {
	http.Handler
	Recorder *testutil.RequestRecorder

	mu             sync.RWMutex
	Collaborations []Collaboration
	Projects       []Project
	Sandboxes      []Sandbox
	RunResponse    *RunResponse
	AccessToken    string
	AddKeyURL      string
	ExecResponse   *ExecResponse
	RunStatusCode  int // override status code for trigger run endpoint
}

func NewFakeCircleCI() *FakeCircleCI {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	rec := testutil.NewRecorder()
	f := &FakeCircleCI{
		Handler:     r,
		Recorder:    rec,
		AccessToken: "fake-access-token-123",
		AddKeyURL:   "sandbox-abc.example.com",
	}

	r.Use(rec.GinMiddleware())

	// Existing endpoints
	r.GET("/api/v2/me/collaborations", f.handleCollaborations)
	r.GET("/api/v1.1/projects", f.handleProjects)

	// Sandbox endpoints
	r.GET("/api/v2/sandboxes", f.handleListSandboxes)
	r.POST("/api/v2/sandboxes", f.handleCreateSandbox)
	r.POST("/api/v2/sandboxes/:id/access_token", f.handleAccessToken)
	r.POST("/api/v2/sandboxes/ssh/add-key", f.handleAddSshKey)
	r.POST("/api/v2/sandboxes/exec", f.handleExec)

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

func (f *FakeCircleCI) requireBearer(c *gin.Context) bool {
	auth := c.GetHeader("Authorization")
	if auth == "" {
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
	c.JSON(http.StatusOK, gin.H{"sandboxes": filtered})
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

	c.JSON(http.StatusOK, sandbox)
}

func (f *FakeCircleCI) handleAccessToken(c *gin.Context) {
	if !f.requireToken(c) {
		return
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	c.JSON(http.StatusOK, gin.H{"access_token": f.AccessToken})
}

func (f *FakeCircleCI) handleAddSshKey(c *gin.Context) {
	if !f.requireBearer(c) {
		return
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	c.JSON(http.StatusOK, gin.H{"url": f.AddKeyURL})
}

func (f *FakeCircleCI) handleExec(c *gin.Context) {
	if !f.requireBearer(c) {
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

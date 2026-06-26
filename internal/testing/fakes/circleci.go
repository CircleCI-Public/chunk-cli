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

type Sidecar struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	OrgID    string `json:"org_id"`
	Provider string `json:"provider,omitempty"`
	Image    string `json:"image,omitempty"`
}

type Snapshot struct {
	ID    string `json:"id"`
	OrgID string `json:"org_id"`
	Name  string `json:"name"`
	Tag   string `json:"tag,omitempty"`
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

type CommandResponse struct {
	ID        string  `json:"id"`
	CreatedAt string  `json:"created_at"`
	EndedAt   *string `json:"ended_at,omitempty"`
	ExitCode  *int    `json:"exit_code,omitempty"`
	Outcome   *string `json:"outcome,omitempty"`
	Phase     string  `json:"phase"`
	SidecarID string  `json:"sidecar_id"`
}

// FakeCircleCI serves canned responses for the CircleCI API.
type FakeCircleCI struct {
	http.Handler
	Recorder *recorder.RequestRecorder

	mu              sync.RWMutex
	snapshotCounter int
	Collaborations  []Collaboration
	Projects        []Project
	Sidecars        []Sidecar
	Snapshots       []Snapshot
	RunResponse     *RunResponse
	AddKeyURL       string
	ExecResponse    *ExecResponse
	CommandResponse *CommandResponse
	RunStatusCode   int // override status code for trigger run endpoint

	// Per-endpoint status code overrides for testing error responses.
	CollaborationsStatusCode int // override for GET /me/collaborations
	ListStatusCode           int // override for GET /sidecar/instances
	CreateStatusCode         int // override for POST /sidecar/instances
	DeleteStatusCode         int // override for DELETE /sidecar/instances/:id
	ExecStatusCode           int // override for POST /sidecar/instances/:id/exec
	AddKeyStatusCode         int // override for POST /sidecar/instances/:id/ssh/add-key
	CreateSnapshotStatusCode int // override for POST /sidecar/snapshots
	GetSnapshotStatusCode    int // override for GET /sidecar/snapshots/:id
	ListSnapshotsStatusCode  int // override for GET /sidecar/snapshots
	GetCommandStatusCode     int // override for GET /sidecar/commands/:id
}

func NewFakeCircleCI() *FakeCircleCI {
	r, rec := newRouter()
	f := &FakeCircleCI{
		Handler:   r,
		Recorder:  rec,
		AddKeyURL: "sidecar-abc.example.com",
	}

	// Existing endpoints
	r.GET("/api/v2/me", f.handleGetCurrentUser)
	r.GET("/api/v2/me/collaborations", f.handleCollaborations)
	r.GET("/api/v1.1/projects", f.handleProjects)

	// Sidecar V3 endpoints
	r.GET("/api/v3/sidecar/instances", f.handleListSidecars)
	r.POST("/api/v3/sidecar/instances", f.handleCreateSidecar)
	r.DELETE("/api/v3/sidecar/instances/:id", f.handleDeleteSidecar)
	r.POST("/api/v3/sidecar/instances/:id/ssh/add-key", f.handleAddSSHKey)
	r.POST("/api/v3/sidecar/instances/:id/exec", f.handleExec)

	// Snapshot V3 endpoints
	r.GET("/api/v3/sidecar/snapshots", f.handleListSnapshots)
	r.POST("/api/v3/sidecar/snapshots", f.handleCreateSnapshot)
	r.GET("/api/v3/sidecar/snapshots/:id", f.handleGetSnapshot)

	// Command V3 endpoint
	r.GET("/api/v3/sidecar/commands/:id", f.handleGetCommand)

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

func (f *FakeCircleCI) handleListSidecars(c *gin.Context) {
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
	var items []gin.H
	for _, s := range f.Sidecars {
		if s.OrgID == orgID {
			items = append(items, gin.H{
				"attributes": gin.H{"name": s.Name},
				"id":         s.ID,
				"references": gin.H{
					"org": gin.H{"id": s.OrgID},
				},
			})
		}
	}
	if items == nil {
		items = []gin.H{}
	}
	c.JSON(http.StatusOK, gin.H{"data": items})
}

func (f *FakeCircleCI) handleCreateSidecar(c *gin.Context) {
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
		Data struct {
			Attributes struct {
				Name  string `json:"name"`
				Image string `json:"image,omitempty"`
			} `json:"attributes"`
			References struct {
				Org struct {
					ID string `json:"id"`
				} `json:"org"`
			} `json:"references"`
		} `json:"data"`
	}
	if err := json.NewDecoder(c.Request.Body).Decode(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Bad request"})
		return
	}

	sidecar := Sidecar{
		ID:    "sidecar-new-123",
		Name:  body.Data.Attributes.Name,
		OrgID: body.Data.References.Org.ID,
		Image: body.Data.Attributes.Image,
	}

	f.mu.Lock()
	f.Sidecars = append(f.Sidecars, sidecar)
	f.mu.Unlock()

	c.JSON(http.StatusCreated, gin.H{
		"data": gin.H{
			"attributes": gin.H{"name": sidecar.Name},
			"id":         sidecar.ID,
			"references": gin.H{
				"org":  gin.H{"id": sidecar.OrgID},
				"user": gin.H{"id": "user-123"},
			},
		},
	})
}

func (f *FakeCircleCI) handleDeleteSidecar(c *gin.Context) {
	if !f.requireToken(c) {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.DeleteStatusCode != 0 {
		c.JSON(f.DeleteStatusCode, gin.H{"message": "API error"})
		return
	}
	id := c.Param("id")
	kept := f.Sidecars[:0]
	for _, s := range f.Sidecars {
		if s.ID != id {
			kept = append(kept, s)
		}
	}
	f.Sidecars = kept
	c.Status(http.StatusNoContent)
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
	c.JSON(http.StatusOK, gin.H{
		"data": gin.H{
			"attributes": gin.H{"url": f.AddKeyURL},
			"id":         c.Param("id"),
		},
	})
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

	if resp == nil {
		resp = &ExecResponse{
			CommandID: "cmd-123",
			PID:       42,
			Stdout:    "ok\n",
			Stderr:    "",
			ExitCode:  0,
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"data": gin.H{
			"attributes": gin.H{
				"exit_code": resp.ExitCode,
				"pid":       resp.PID,
				"stdout":    resp.Stdout,
				"stderr":    resp.Stderr,
			},
			"id": resp.CommandID,
			"references": gin.H{
				"sidecar_instance": gin.H{"id": c.Param("id")},
			},
		},
	})
}

func (f *FakeCircleCI) handleGetCommand(c *gin.Context) {
	if !f.requireToken(c) {
		return
	}
	f.mu.RLock()
	defer f.mu.RUnlock()

	if f.GetCommandStatusCode != 0 {
		c.JSON(f.GetCommandStatusCode, gin.H{"message": "API error"})
		return
	}

	resp := f.CommandResponse
	if resp == nil {
		resp = &CommandResponse{
			ID:        c.Param("id"),
			CreatedAt: "2025-01-15T10:00:00.000Z",
			Phase:     "ended",
			SidecarID: "sb-1",
		}
	}

	attrs := gin.H{
		"created_at": resp.CreatedAt,
		"phase":      resp.Phase,
	}
	if resp.EndedAt != nil {
		attrs["ended_at"] = *resp.EndedAt
	}
	if resp.ExitCode != nil {
		attrs["exit_code"] = *resp.ExitCode
	}
	if resp.Outcome != nil {
		attrs["outcome"] = *resp.Outcome
	}

	c.JSON(http.StatusOK, gin.H{
		"data": gin.H{
			"attributes": attrs,
			"id":         resp.ID,
			"references": gin.H{
				"sidecar_instance": gin.H{"id": resp.SidecarID},
			},
		},
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
		Data struct {
			Attributes struct {
				Name string `json:"name"`
			} `json:"attributes"`
			References struct {
				SidecarInstance struct {
					ID string `json:"id"`
				} `json:"sidecar_instance"`
			} `json:"references"`
		} `json:"data"`
	}
	if err := json.NewDecoder(c.Request.Body).Decode(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Bad request"})
		return
	}

	sidecarID := body.Data.References.SidecarInstance.ID

	f.mu.Lock()
	f.snapshotCounter++
	snap := Snapshot{
		ID:   fmt.Sprintf("snap-%d", f.snapshotCounter),
		Name: body.Data.Attributes.Name,
	}
	var orgID string
	for _, s := range f.Sidecars {
		if s.ID == sidecarID {
			orgID = s.OrgID
			break
		}
	}
	f.Snapshots = append(f.Snapshots, snap)
	f.mu.Unlock()

	c.JSON(http.StatusCreated, gin.H{
		"data": gin.H{
			"attributes": gin.H{"name": snap.Name},
			"id":         snap.ID,
			"references": gin.H{
				"org": gin.H{"id": orgID},
			},
		},
	})
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
			attrs := gin.H{"name": s.Name}
			if s.Tag != "" {
				attrs["tag"] = s.Tag
			}
			c.JSON(http.StatusOK, gin.H{
				"data": gin.H{
					"attributes": attrs,
					"id":         s.ID,
					"references": gin.H{
						"org": gin.H{"id": s.OrgID},
					},
				},
			})
			return
		}
	}
	c.JSON(http.StatusNotFound, gin.H{"message": "snapshot not found"})
}

func (f *FakeCircleCI) handleListSnapshots(c *gin.Context) {
	if !f.requireToken(c) {
		return
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	if f.ListSnapshotsStatusCode != 0 {
		c.JSON(f.ListSnapshotsStatusCode, gin.H{"message": "API error"})
		return
	}
	orgID := c.Query("org_id")
	var items []gin.H
	for _, s := range f.Snapshots {
		if s.OrgID == orgID {
			attrs := gin.H{"name": s.Name, "is_system": false}
			if s.Tag != "" {
				attrs["tag"] = s.Tag
			}
			items = append(items, gin.H{
				"id":         s.ID,
				"attributes": attrs,
				"references": gin.H{"org": gin.H{"id": s.OrgID}},
			})
		}
	}
	if items == nil {
		items = []gin.H{}
	}
	c.JSON(http.StatusOK, gin.H{"data": items})
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

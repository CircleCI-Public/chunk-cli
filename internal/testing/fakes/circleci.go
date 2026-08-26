package fakes

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
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
	ID       string `json:"id"`
	OrgID    string `json:"org_id"`
	Name     string `json:"name"`
	Tag      string `json:"tag,omitempty"`
	IsSystem bool   `json:"is_system,omitempty"`
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
	Signal    string `json:"signal,omitempty"`
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
	orgCounter      int
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
	CollaborationsStatusCode int    // override for GET /me/collaborations
	ListStatusCode           int    // override for GET /sidecar/instances
	CreateStatusCode         int    // override for POST /sidecar/instances
	DeleteStatusCode         int    // override for DELETE /sidecar/instances/:id
	PruneStatusCode          int    // override for POST /sidecar/instances/prune
	ExecStatusCode           int    // override for POST /sidecar/instances/:id/exec
	ExecMessage              string // V3 error title when ExecStatusCode is set
	CommandOutputStatusCode  int    // override for GET /sidecar/commands/:id/output
	CommandOutputMessage     string // error message body when CommandOutputStatusCode is set
	// DropStreamsBeforeExit ends this many output streams after delivering their
	// output but before the terminal event, so client resume can be exercised.
	DropStreamsBeforeExit int
	// EmptyStreamsBeforeExit returns this many completely empty output responses
	// (status 200, no body) before serving the real stream. This simulates an
	// interrupted connection where no SSE frames arrive at all, which the client
	// must treat as a resume trigger rather than a failure.
	EmptyStreamsBeforeExit   int
	AddKeyStatusCode         int // override for POST /sidecar/instances/:id/ssh/add-key
	CreateSnapshotStatusCode int // override for POST /sidecar/snapshots
	GetSnapshotStatusCode    int // override for GET /sidecar/snapshots/:id
	ListSnapshotsStatusCode  int // override for GET /sidecar/snapshots
	GetCommandStatusCode     int // override for GET /sidecar/commands/:id
	CreateOrgStatusCode      int // override for POST /api/v2/organization

	// ExtraHeaders are added to every response. Use to inject Deprecation/Sunset
	// headers without changing individual handler logic.
	ExtraHeaders http.Header
}

// ServeHTTP injects ExtraHeaders into every response before delegating to the
// embedded gin handler. Headers must be set before the first Write or
// WriteHeader call, which gin does internally — setting them here satisfies
// that ordering.
func (f *FakeCircleCI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.RLock()
	for k, vals := range f.ExtraHeaders {
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	f.mu.RUnlock()
	f.Handler.ServeHTTP(w, r)
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
	r.POST("/api/v3/sidecar/instances/prune", f.handlePruneSidecars)
	r.DELETE("/api/v3/sidecar/instances/:id", f.handleDeleteSidecar)
	r.POST("/api/v3/sidecar/instances/:id/ssh/add-key", f.handleAddSSHKey)
	r.POST("/api/v3/sidecar/instances/:id/exec", f.handleExec)

	// Snapshot V3 endpoints
	r.GET("/api/v3/sidecar/snapshots", f.handleListSnapshots)
	r.POST("/api/v3/sidecar/snapshots", f.handleCreateSnapshot)
	r.GET("/api/v3/sidecar/snapshots/:id", f.handleGetSnapshot)

	// Command V3 endpoints
	r.GET("/api/v3/sidecar/commands/:id", f.handleGetCommand)
	r.GET("/api/v3/sidecar/commands/:id/output", f.handleCommandOutput)

	// Task run endpoint
	r.POST("/api/v2/agents/org/:org_id/project/:project_id/runs", f.handleTriggerRun)

	// Org endpoint
	r.POST("/api/v2/organization", f.handleCreateOrg)

	return f
}

func (f *FakeCircleCI) handleGetCurrentUser(c *gin.Context) {
	if !f.requireToken(c) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": "00000000-0000-0000-0000-000000000123", "login": "testuser"})
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

func (f *FakeCircleCI) handlePruneSidecars(c *gin.Context) {
	if !f.requireToken(c) {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.PruneStatusCode != 0 {
		c.JSON(f.PruneStatusCode, gin.H{"message": "API error"})
		return
	}
	var body struct {
		OrgID string `json:"org_id"`
	}
	if err := json.NewDecoder(c.Request.Body).Decode(&body); err != nil || body.OrgID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"title": "Not found"}})
		return
	}
	kept := f.Sidecars[:0]
	deleted := 0
	for _, s := range f.Sidecars {
		if s.OrgID == body.OrgID {
			deleted++
		} else {
			kept = append(kept, s)
		}
	}
	f.Sidecars = kept
	c.JSON(http.StatusOK, gin.H{
		"data": gin.H{
			"attributes": gin.H{"deleted_count": deleted},
		},
	})
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
	msg := f.ExecMessage
	f.mu.RUnlock()

	if statusCode != 0 {
		if msg == "" {
			msg = "API error"
		}
		// The V3 envelope shape, which is what this route really returns; the
		// client has to dig the title out of it to say anything useful.
		c.JSON(statusCode, gin.H{"error": gin.H{"id": "trace-1", "title": msg}})
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

	// Async: return 202 with command ID; output is streamed via GET /commands/:id/output.
	c.JSON(http.StatusAccepted, gin.H{
		"data": gin.H{
			"attributes": gin.H{"phase": "received"},
			"id":         resp.CommandID,
			"references": gin.H{
				"sidecar_instance": gin.H{"id": c.Param("id")},
			},
		},
	})
}

func (f *FakeCircleCI) handleCommandOutput(c *gin.Context) {
	if !f.requireToken(c) {
		return
	}
	f.mu.RLock()
	resp := f.ExecResponse
	statusCode := f.CommandOutputStatusCode
	msg := f.CommandOutputMessage
	f.mu.RUnlock()

	if statusCode != 0 {
		if msg == "" {
			msg = "API error"
		}
		c.JSON(statusCode, gin.H{"message": msg})
		return
	}

	if f.emptyStreamBeforeExit() {
		// Return 200 with no body to simulate a connection interrupted before
		// any SSE frames were written. The client must treat this as a resume.
		c.Status(http.StatusOK)
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

	// Mirror the real API: SSE frames with base64 payloads and an opaque cursor.
	stdout := []byte(resp.Stdout)
	stderr := []byte(resp.Stderr)
	outOff := clampOffset(cursorPart(c, 0), len(stdout))
	errOff := clampOffset(cursorPart(c, 1), len(stderr))

	c.Header("Content-Type", "text/event-stream; charset=utf-8")
	c.Header("Cache-Control", "no-cache")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)

	// Flushing per frame is what makes this a stream rather than one lump at the
	// end — without it nothing here proves incremental delivery.
	writeFrame := func(event, id string, data []byte) {
		_, _ = fmt.Fprintf(c.Writer, "event: %s\nid: %s\ndata: %s\n\n", event, id, data)
		c.Writer.Flush()
	}

	cursor := func() string { return fmt.Sprintf("%d,%d", outOff, errOff) }

	start, _ := json.Marshal(map[string]any{
		"command_id": resp.CommandID,
		"status":     "running",
		"stdout":     outOff,
		"stderr":     errOff,
	})
	writeFrame("start", cursor(), start)

	if rest := stdout[outOff:]; len(rest) > 0 {
		outOff = int64(len(stdout))
		writeFrame("stdout", cursor(), []byte(base64.StdEncoding.EncodeToString(rest)))
	}
	if rest := stderr[errOff:]; len(rest) > 0 {
		errOff = int64(len(stderr))
		writeFrame("stderr", cursor(), []byte(base64.StdEncoding.EncodeToString(rest)))
	}

	if f.dropBeforeExit() {
		// End the response with no terminal event, which is how the API signals
		// "interrupted, resume from your cursor".
		return
	}

	exit, _ := json.Marshal(map[string]any{
		"exit_code": resp.ExitCode,
		"pid":       resp.PID,
		"signal":    resp.Signal,
	})
	writeFrame("exit", cursor(), exit)
}

// dropBeforeExit reports whether this stream should end without a terminal
// event, consuming one from the configured budget.
func (f *FakeCircleCI) dropBeforeExit() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.DropStreamsBeforeExit <= 0 {
		return false
	}
	f.DropStreamsBeforeExit--
	return true
}

// emptyStreamBeforeExit reports whether this request should return an empty
// body (no SSE frames), consuming one from the configured budget.
func (f *FakeCircleCI) emptyStreamBeforeExit() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.EmptyStreamsBeforeExit <= 0 {
		return false
	}
	f.EmptyStreamsBeforeExit--
	return true
}

// cursorPart resolves one stream's resume offset from Last-Event-ID, falling
// back to the per-stream query parameters, mirroring the real API.
func cursorPart(c *gin.Context, part int) int64 {
	if raw := c.GetHeader("Last-Event-ID"); raw != "" {
		out, errOff, found := strings.Cut(raw, ",")
		if found {
			if part == 0 {
				return parseInt64(out)
			}
			return parseInt64(errOff)
		}
	}
	if part == 0 {
		return parseInt64(c.Query("stdout_offset"))
	}
	return parseInt64(c.Query("stderr_offset"))
}

func parseInt64(s string) int64 {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func clampOffset(off int64, size int) int64 {
	return min(max(off, 0), int64(size))
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
			attrs := gin.H{"name": s.Name, "is_system": s.IsSystem}
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

func (f *FakeCircleCI) handleCreateOrg(c *gin.Context) {
	if !f.requireToken(c) {
		return
	}

	f.mu.Lock()
	statusCode := f.CreateOrgStatusCode
	f.mu.Unlock()

	if statusCode != 0 {
		c.JSON(statusCode, gin.H{"message": "API error"})
		return
	}

	var body struct {
		Name    string `json:"name"`
		VCSType string `json:"vcs_type"`
	}
	if err := json.NewDecoder(c.Request.Body).Decode(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Bad request"})
		return
	}

	f.mu.Lock()
	f.orgCounter++
	id := fmt.Sprintf("org-new-%d", f.orgCounter)
	f.mu.Unlock()

	c.JSON(http.StatusCreated, gin.H{
		"id":       id,
		"name":     body.Name,
		"slug":     body.Name,
		"vcs_type": body.VCSType,
	})
}

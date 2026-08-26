package daemon

import (
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const jsonKeyError = "error"

// Server is the daemon HTTP server. It holds validation/sync state and
// broadcasts events to SSE subscribers.
type Server struct {
	st     *state
	h      *hub
	router *gin.Engine
}

// NewServer creates a new daemon Server.
func NewServer() *Server {
	gin.SetMode(gin.ReleaseMode)
	s := &Server{
		st: newState(),
		h:  newHub(),
	}
	r := gin.New()
	r.GET("/events", s.handleEvents)
	api := r.Group("/api")
	api.POST("/invocations", s.handleStartInvocation)
	api.POST("/invocations/:id/steps", s.handleInvocationStep)
	api.POST("/invocations/:id/finish", s.handleFinishInvocation)
	api.PUT("/sidecars/:id", s.handleUpsertSidecar)
	s.router = r
	return s
}

// Run starts the server on addr (e.g. "127.0.0.1:7777").
func (s *Server) Run(addr string) error {
	return s.router.Run(addr)
}

func (s *Server) handleEvents(c *gin.Context) {
	ch, unsub := s.h.subscribe()
	defer unsub()
	snap := s.st.snapshot()
	snapshotSent := false
	c.Stream(func(_ io.Writer) bool {
		if !snapshotSent {
			c.SSEvent("snapshot", snap)
			snapshotSent = true
			return true
		}
		select {
		case e, ok := <-ch:
			if !ok {
				return false
			}
			c.SSEvent(e.Type, e.Data)
			return true
		case <-c.Request.Context().Done():
			return false
		}
	})
}

type startInvocationReq struct {
	SidecarID string `json:"sidecar_id" binding:"required"`
	Op        string `json:"op"         binding:"required"`
	Branch    string `json:"branch"`
}

func (s *Server) handleStartInvocation(c *gin.Context) {
	var req startInvocationReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{jsonKeyError: err.Error()})
		return
	}
	inv := InvocationState{
		ID:        uuid.New().String(),
		SidecarID: req.SidecarID,
		Op:        req.Op,
		Branch:    req.Branch,
		StartedAt: time.Now(),
	}
	s.st.startInvocation(inv)
	s.h.broadcast(Event{Type: "invocation_started", Data: inv})
	c.JSON(http.StatusOK, gin.H{"invocation_id": inv.ID})
}

type invocationStepReq struct {
	Level string `json:"level" binding:"required"`
	Msg   string `json:"msg"`
}

func (s *Server) handleInvocationStep(c *gin.Context) {
	id := c.Param("id")
	var req invocationStepReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{jsonKeyError: err.Error()})
		return
	}
	s.h.broadcast(Event{
		Type: "invocation_step",
		Data: map[string]string{"invocation_id": id, "level": req.Level, jsonKeyMsg: req.Msg},
	})
	c.Status(http.StatusNoContent)
}

type finishInvocationReq struct {
	Passed     int    `json:"passed"`
	Total      int    `json:"total"`
	DurationMs int64  `json:"duration_ms"`
	OK         bool   `json:"ok"`
	Msg        string `json:"msg"`
}

func (s *Server) handleFinishInvocation(c *gin.Context) {
	id := c.Param("id")
	var req finishInvocationReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{jsonKeyError: err.Error()})
		return
	}
	sum := &InvocationSummary{
		ID:         id,
		Passed:     req.Passed,
		Total:      req.Total,
		DurationMs: req.DurationMs,
		OK:         req.OK,
		Msg:        req.Msg,
		FinishedAt: time.Now(),
	}
	s.st.finishInvocation(sum)
	s.h.broadcast(Event{Type: "invocation_finished", Data: sum})
	c.Status(http.StatusNoContent)
}

type upsertSidecarReq struct {
	Name          string    `json:"name"`
	SyncState     SyncState `json:"sync_state"`
	LastSyncedRef string    `json:"last_synced_ref"`
}

func (s *Server) handleUpsertSidecar(c *gin.Context) {
	id := c.Param("id")
	var req upsertSidecarReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{jsonKeyError: err.Error()})
		return
	}
	sc := SidecarState{
		ID:            id,
		Name:          req.Name,
		SyncState:     req.SyncState,
		LastSyncedRef: req.LastSyncedRef,
	}
	s.st.upsertSidecar(sc)
	s.h.broadcast(Event{Type: "sidecar_updated", Data: sc})
	c.Status(http.StatusNoContent)
}

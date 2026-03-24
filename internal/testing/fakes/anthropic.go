package fakes

import (
	"fmt"
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"

	"github.com/CircleCI-Public/chunk-cli/internal/testing/recorder"
)

// FakeAnthropic serves canned responses for the Anthropic Messages API.
type FakeAnthropic struct {
	http.Handler
	Recorder *recorder.RequestRecorder

	mu        sync.Mutex
	responses []string // queued text responses, dequeued in FIFO order
	callIndex int
}

func NewFakeAnthropic(responses ...string) *FakeAnthropic {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	rec := recorder.NewRecorder()
	f := &FakeAnthropic{
		Handler:   r,
		Recorder:  rec,
		responses: responses,
	}

	r.Use(rec.GinMiddleware())
	r.POST("/v1/messages", f.handleMessages)
	r.POST("/v1/messages/count_tokens", f.handleCountTokens)
	return f
}

func (f *FakeAnthropic) handleMessages(c *gin.Context) {
	apiKey := c.GetHeader("x-api-key")
	if apiKey == "" {
		c.JSON(http.StatusUnauthorized, gin.H{
			"type":    "error",
			"error":   gin.H{"type": "authentication_error", "message": "invalid x-api-key"},
			"message": "invalid x-api-key",
		})
		return
	}

	f.mu.Lock()
	idx := f.callIndex
	f.callIndex++
	f.mu.Unlock()

	text := "default response"
	if idx < len(f.responses) {
		text = f.responses[idx]
	}

	c.JSON(http.StatusOK, gin.H{
		"id":          fmt.Sprintf("msg_%03d", idx),
		"type":        "message",
		"role":        "assistant",
		"model":       "claude-sonnet-4-5-20250929",
		"stop_reason": "end_turn",
		"content": []gin.H{
			{"type": "text", "text": text},
		},
		"usage": gin.H{
			"input_tokens":  100,
			"output_tokens": 200,
		},
	})
}

func (f *FakeAnthropic) handleCountTokens(c *gin.Context) {
	apiKey := c.GetHeader("x-api-key")
	if apiKey == "" {
		c.JSON(http.StatusUnauthorized, gin.H{
			"type":    "error",
			"error":   gin.H{"type": "authentication_error", "message": "invalid x-api-key"},
			"message": "invalid x-api-key",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{"input_tokens": 10})
}

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

	// tokenLimitErrors is the number of initial calls that return a
	// "prompt is too long" error before succeeding.
	tokenLimitErrors int
}

func NewFakeAnthropic(responses ...string) *FakeAnthropic {
	r, rec := newRouter()
	f := &FakeAnthropic{
		Handler:   r,
		Recorder:  rec,
		responses: responses,
	}

	r.POST("/v1/messages", f.handleMessages)
	r.POST("/v1/messages/count_tokens", f.handleCountTokens)
	return f
}

// SetTokenLimitErrors configures the fake to return n consecutive "prompt is
// too long" errors before returning normal responses.
func (f *FakeAnthropic) SetTokenLimitErrors(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokenLimitErrors = n
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

	if c.GetHeader("anthropic-version") == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"type":    "error",
			"error":   gin.H{"type": "invalid_request_error", "message": "missing required header: anthropic-version"},
			"message": "missing required header: anthropic-version",
		})
		return
	}

	f.mu.Lock()
	remainingErrors := f.tokenLimitErrors
	if remainingErrors > 0 {
		f.tokenLimitErrors--
	}
	idx := f.callIndex
	if remainingErrors == 0 {
		f.callIndex++
	}
	f.mu.Unlock()

	if remainingErrors > 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"type": "error",
			"error": gin.H{
				"type":    "invalid_request_error",
				"message": "prompt is too long: 200000 tokens > 100000 maximum",
			},
			"message": "prompt is too long: 200000 tokens > 100000 maximum",
		})
		return
	}

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

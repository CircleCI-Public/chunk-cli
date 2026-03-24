package testutil

import (
	"bytes"
	"io"
	"net/http"
	"net/url"
	"sync"

	"github.com/gin-gonic/gin"
)

// RecordedRequest is a snapshot of an HTTP request.
type RecordedRequest struct {
	Method string
	URL    url.URL
	Header http.Header
	Body   []byte
}

// RequestRecorder captures HTTP requests for test assertions.
type RequestRecorder struct {
	mu       sync.RWMutex
	requests []RecordedRequest
}

// NewRecorder creates a new RequestRecorder.
func NewRecorder() *RequestRecorder {
	return &RequestRecorder{}
}

func (r *RequestRecorder) record(req *http.Request) {
	rec := RecordedRequest{
		Method: req.Method,
		URL:    *req.URL,
		Header: req.Header.Clone(),
	}
	if req.Body != nil {
		body, err := io.ReadAll(req.Body)
		if err == nil {
			rec.Body = body
		}
		req.Body = io.NopCloser(bytes.NewReader(body))
	}
	r.mu.Lock()
	r.requests = append(r.requests, rec)
	r.mu.Unlock()
}

// AllRequests returns a copy of all recorded requests.
func (r *RequestRecorder) AllRequests() []RecordedRequest {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]RecordedRequest, len(r.requests))
	copy(out, r.requests)
	return out
}

// GinMiddleware returns gin middleware that records every request.
func (r *RequestRecorder) GinMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		r.record(c.Request)
		c.Next()
	}
}

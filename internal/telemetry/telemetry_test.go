package telemetry_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/telemetry"
)

const goodWriteKey = "b4b250188e5994cf45e7b0e5"

func TestClient_Track(t *testing.T) {
	instanceID := uuid.NewString()

	fs := newFakeSegment(goodWriteKey)
	srv := httptest.NewServer(fs)
	t.Cleanup(srv.Close)

	ac, err := telemetry.New(context.Background(), telemetry.Config{
		Mode:      telemetry.ModeSend,
		Endpoint:  srv.URL,
		WriteKey:  goodWriteKey,
		BatchSize: 2,
		User: telemetry.User{
			InstanceID: instanceID,
			OS:         "linux",
			Arch:       "amd64",
			Version:    "1.0.0",
		},
	})
	assert.NilError(t, err)

	assert.NilError(t, ac.Identify())
	assert.NilError(t, ac.Track("build-prompt", map[string]any{"success": true}))
	assert.NilError(t, ac.Close())

	var allMsgs []batchMessage
	for _, b := range fs.Batches() {
		allMsgs = append(allMsgs, b.Messages...)
	}
	assert.Assert(t, len(allMsgs) == 2, "expected 2 messages, got %d", len(allMsgs))

	var identifyMsg, trackMsg *batchMessage
	for i := range allMsgs {
		switch allMsgs[i].Type {
		case "identify":
			identifyMsg = &allMsgs[i]
		case "track":
			trackMsg = &allMsgs[i]
		}
	}

	assert.Assert(t, identifyMsg != nil, "expected an identify message")
	assert.Equal(t, identifyMsg.AnonymousID, instanceID)

	assert.Assert(t, trackMsg != nil, "expected a track message")
	assert.Equal(t, trackMsg.Event, "build-prompt")
	assert.Equal(t, trackMsg.AnonymousID, instanceID)
	assert.Check(t, cmp.DeepEqual(trackMsg.Properties, map[string]any{"success": true}))
}

func TestClient_ModeNOOP(t *testing.T) {
	ac, err := telemetry.New(context.Background(), telemetry.Config{Mode: telemetry.ModeNOOP})
	assert.NilError(t, err)
	assert.Assert(t, ac != nil)
	assert.NilError(t, ac.Identify())
	assert.NilError(t, ac.Track("build-prompt", nil))
	assert.NilError(t, ac.Close())
}

type batchMessage struct {
	Type        string         `json:"type"`
	AnonymousID string         `json:"anonymousId"`
	Event       string         `json:"event"`
	Properties  map[string]any `json:"properties"`
}

type batch struct {
	SentAt   time.Time      `json:"sentAt"`
	Messages []batchMessage `json:"batch"`
}

func newFakeSegment(apiKey string) *fakeSegment {
	fs := &fakeSegment{apiKey: basicAuth(apiKey, "")}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/batch", fs.handleBatch)
	fs.Handler = mux
	return fs
}

type fakeSegment struct {
	http.Handler

	apiKey  string
	batches []batch
	mu      sync.RWMutex
}

func (s *fakeSegment) handleBatch(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Basic "+s.apiKey {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	var b batch
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	s.batches = append(s.batches, b)
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"success":true}`))
}

func (s *fakeSegment) Batches() []batch {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Clone(s.batches)
}

func basicAuth(username, password string) string {
	return base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
}

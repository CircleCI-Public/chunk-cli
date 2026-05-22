package telemetry_test

import (
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
	knownUserID := uuid.New()

	tests := []struct {
		name       string
		userID     uuid.UUID
		wantUserID string
	}{
		{
			name:       "uses provided user ID",
			userID:     knownUserID,
			wantUserID: knownUserID.String(),
		},
		{
			name:       "falls back to anonymous ID",
			wantUserID: telemetry.AnonymousID.String(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := newFakeSegment(goodWriteKey)
			srv := httptest.NewServer(fs)
			t.Cleanup(srv.Close)

			ac, err := telemetry.New(telemetry.Config{
				Endpoint: srv.URL,
				WriteKey: goodWriteKey,
				User: telemetry.User{
					InstanceID: uuid.New(),
					UserID:     tt.userID,
					OS:         "linux",
					Version:    "1.0.0",
				},
			})
			assert.NilError(t, err)

			assert.NilError(t, ac.Identify())
			assert.NilError(t, ac.Track("build-prompt", map[string]any{"success": true}))
			assert.NilError(t, ac.Close())

			batches := fs.Batches()
			assert.Assert(t, len(batches) == 1, "expected 1 batch, got %d", len(batches))
			msgs := batches[0].Messages
			assert.Assert(t, len(msgs) == 2, "expected 2 messages, got %d", len(msgs))

			assert.Equal(t, msgs[0].Type, "identify")
			assert.Equal(t, msgs[0].UserID, tt.wantUserID)

			assert.Equal(t, msgs[1].Type, "track")
			assert.Equal(t, msgs[1].Event, "build-prompt")
			assert.Equal(t, msgs[1].UserID, tt.wantUserID)
			assert.Check(t, cmp.DeepEqual(msgs[1].Properties, map[string]any{"success": true}))
		})
	}
}

func TestClient_NilSafe(t *testing.T) {
	ac, err := telemetry.New(telemetry.Config{})
	assert.NilError(t, err)
	assert.NilError(t, ac.Track("build-prompt", nil))
}

type batchMessage struct {
	Type       string         `json:"type"`
	UserID     string         `json:"userId"`
	Event      string         `json:"event"`
	Properties map[string]any `json:"properties"`
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

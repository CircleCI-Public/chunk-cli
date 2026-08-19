package circleci

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
)

func TestSidecarGone(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "404 naming the sidecar",
			err:  &StatusError{Op: "exec", StatusCode: http.StatusNotFound, ServerMessage: "Not found"},
			want: true,
		},
		{
			// The gateway's wording for an unknown route. Treating it as a
			// missing sidecar would prune healthy local state.
			name: "404 from a missing route",
			err:  &StatusError{Op: "exec", StatusCode: http.StatusNotFound, ServerMessage: "Route Not Found."},
			want: false,
		},
		{
			name: "404 with no message at all",
			err:  &StatusError{Op: "exec", StatusCode: http.StatusNotFound},
			want: true,
		},
		{
			name: "wrapped 404 still matches",
			err:  fmt.Errorf("sync: %w", &StatusError{Op: "sync", StatusCode: http.StatusNotFound}),
			want: true,
		},
		{
			name: "409 is not gone",
			err:  &StatusError{Op: "exec", StatusCode: http.StatusConflict, ServerMessage: "sidecar is paused"},
			want: false,
		},
		{
			name: "unrelated error",
			err:  errors.New("dial tcp: connection refused"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, SidecarGone(tt.err), tt.want)
		})
	}
}

func TestSidecarPaused(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "409 naming the condition",
			err:  &StatusError{Op: "exec", StatusCode: http.StatusConflict, ServerMessage: "sidecar is paused"},
			want: true,
		},
		{
			name: "wording is matched case-insensitively",
			err:  &StatusError{Op: "exec", StatusCode: http.StatusConflict, ServerMessage: "Sidecar Is Paused"},
			want: true,
		},
		{
			// A conflict the CLI has no specific advice for must fall through to
			// the generic path rather than claim the sidecar is asleep.
			name: "409 for some other conflict",
			err:  &StatusError{Op: "exec", StatusCode: http.StatusConflict, ServerMessage: "command already running"},
			want: false,
		},
		{
			name: "paused wording on a different status",
			err:  &StatusError{Op: "exec", StatusCode: http.StatusGone, ServerMessage: "sidecar is paused"},
			want: false,
		},
		{
			name: "unrelated error",
			err:  errors.New("dial tcp: connection refused"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, SidecarPaused(tt.err), tt.want)
		})
	}
}

// mapErr used to attach the server's message only for 410, so every other
// status reached users as bare status text. These check it for the statuses the
// sidecar routes actually return, in both body shapes the API uses.
func TestExecSurfacesServerMessage(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		message    string
	}{
		{name: "paused sidecar", statusCode: http.StatusConflict, message: "sidecar is paused"},
		{name: "missing sidecar", statusCode: http.StatusNotFound, message: "Not found"},
		{name: "out of date sidecar", statusCode: http.StatusGone, message: "sidecar is out of date"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := fakes.NewFakeCircleCI()
			fake.ExecStatusCode = tt.statusCode
			fake.ExecMessage = tt.message
			srv := httptest.NewServer(fake)
			defer srv.Close()

			client := newTestClient(t, srv.URL)
			_, err := client.Exec(context.Background(), "sb-1", "w", nil, nil, nil)

			var se *StatusError
			assert.Assert(t, errors.As(err, &se), "expected StatusError, got %v", err)
			assert.Equal(t, se.StatusCode, tt.statusCode)
			assert.Equal(t, se.ServerMessage, tt.message)
		})
	}
}

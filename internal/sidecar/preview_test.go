package sidecar

import (
	"context"
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"
)

func TestBuildPreviewURL(t *testing.T) {
	cases := []struct {
		name string
		in   string
		port int
		want string
	}{
		{"https e2b host", "https://8000-abc123.e2b.app", 3000, "https://3000-abc123.e2b.app"},
		{"http bare host", "http://8000-abc123.e2b.app", 3000, "http://3000-abc123.e2b.app"},
		{"ws scheme", "ws://8000-abc123.e2b.app", 3000, "http://3000-abc123.e2b.app"},
		{"host with path", "https://8000-abc123.e2b.app/some/path", 3000, "https://3000-abc123.e2b.app"},
		{"bare host no scheme", "8000-abc123.e2b.app", 3000, "https://3000-abc123.e2b.app"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := BuildPreviewURL(tc.in, tc.port)
			assert.NilError(t, err)
			assert.Equal(t, got, tc.want)
		})
	}
}

func TestBuildPreviewURL_NonConformingHost(t *testing.T) {
	cases := []string{
		"https://localhost",
		"https://abc123.e2b.app",
		"https://notanumber-abc123.e2b.app",
	}
	for _, in := range cases {
		_, err := BuildPreviewURL(in, 3000)
		assert.Assert(t, err != nil, "expected error for input %q", in)
		assert.Assert(t, strings.Contains(err.Error(), "does not look like an e2b sandbox host"))
	}
}

func TestWaitForPort_TimesOut(t *testing.T) {
	session := &Session{URL: "ws://127.0.0.1:1/ssh/tunnel"}
	err := waitForPort(context.Background(), session, 3000, 20*time.Millisecond, 5*time.Millisecond)
	assert.Assert(t, err != nil)
	assert.Assert(t, strings.Contains(err.Error(), "timed out waiting for port 3000"))
}

package validate

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

// sessionID returns a unique session ID for each test to prevent state leakage.
func sessionID(t *testing.T) string {
	t.Helper()
	id := fmt.Sprintf("test-%s", t.Name())
	t.Cleanup(func() { ResetAttempts(id) })
	return id
}

// --- TrackFailedAttempt / ResetAttempts ---

func TestTrackFailedAttempt_Increments(t *testing.T) {
	sid := sessionID(t)
	assert.Equal(t, TrackFailedAttempt(sid, nil), 1)
	assert.Equal(t, TrackFailedAttempt(sid, nil), 2)
	assert.Equal(t, TrackFailedAttempt(sid, nil), 3)
}

func TestTrackFailedAttempt_IndependentSessions(t *testing.T) {
	sid1 := sessionID(t) + "-a"
	sid2 := sessionID(t) + "-b"
	t.Cleanup(func() {
		ResetAttempts(sid1)
		ResetAttempts(sid2)
	})

	TrackFailedAttempt(sid1, nil)
	TrackFailedAttempt(sid1, nil)
	assert.Equal(t, TrackFailedAttempt(sid2, nil), 1, "session 2 should start from 0")
}

func TestResetAttempts_ClearsCounter(t *testing.T) {
	sid := sessionID(t)
	TrackFailedAttempt(sid, nil)
	TrackFailedAttempt(sid, nil)
	ResetAttempts(sid)
	assert.Equal(t, TrackFailedAttempt(sid, nil), 1, "counter should restart after reset")
}

// --- WrapHookResult ---

func TestWrapHookResult_NilError_ResetsAndReturnsNil(t *testing.T) {
	sid := sessionID(t)
	TrackFailedAttempt(sid, nil) // prime the counter
	err := WrapHookResult(sid, nil, DefaultMaxAttempts, nil)
	assert.NilError(t, err)
	// counter should be reset: next failure starts at 1
	assert.Equal(t, TrackFailedAttempt(sid, nil), 1)
}

func TestWrapHookResult_Error_ReturnsExitCode2(t *testing.T) {
	sid := sessionID(t)
	err := WrapHookResult(sid, errors.New("failed"), DefaultMaxAttempts, nil)
	assert.Assert(t, err != nil)
	type exitCoder interface{ ExitCode() int }
	ec, ok := err.(exitCoder)
	assert.Assert(t, ok, "expected ExitCode() method")
	assert.Equal(t, ec.ExitCode(), 2)
}

func TestWrapHookResult_GivesUpAfterMaxAttempts(t *testing.T) {
	sid := sessionID(t)
	var buf bytes.Buffer

	for attempt := 1; attempt < DefaultMaxAttempts; attempt++ {
		err := WrapHookResult(sid, errors.New("failed"), DefaultMaxAttempts, &buf)
		assert.Assert(t, err != nil, "attempt %d: expected exit 2", attempt)
	}
	// final attempt: should give up
	err := WrapHookResult(sid, errors.New("failed"), DefaultMaxAttempts, &buf)
	assert.NilError(t, err, "expected nil after max attempts")
	assert.Assert(t, strings.Contains(buf.String(), "ask the user"), "got: %s", buf.String())
}

func TestWrapHookResult_CustomMaxAttempts(t *testing.T) {
	sid := sessionID(t)
	var buf bytes.Buffer
	err := WrapHookResult(sid, errors.New("failed"), 1, &buf)
	assert.NilError(t, err, "expected give-up after 1 attempt")
	assert.Assert(t, strings.Contains(buf.String(), "ask the user"), "got: %s", buf.String())
}

// --- HooksDisabled ---

func TestHooksDisabled_EnvVar(t *testing.T) {
	assert.Equal(t, HooksDisabled(t.TempDir(), true), true)
}

func TestHooksDisabled_SentinelFile(t *testing.T) {
	dir := t.TempDir()
	assert.NilError(t, os.MkdirAll(filepath.Join(dir, ".chunk"), 0o755))
	assert.NilError(t, os.WriteFile(filepath.Join(dir, ".chunk", "hooks-disabled"), []byte{}, 0o644))
	assert.Equal(t, HooksDisabled(dir, false), true)
}

func TestHooksDisabled_Neither(t *testing.T) {
	assert.Equal(t, HooksDisabled(t.TempDir(), false), false)
}

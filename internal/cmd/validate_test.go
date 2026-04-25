package cmd

import (
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
)

func TestDetectHookParsesSessionID(t *testing.T) {
	payload := `{"session_id":"sess-abc","stop_hook_active":false}`
	got := detectHook(strings.NewReader(payload))
	assert.Assert(t, got != nil)
	assert.Equal(t, got.sessionID, "sess-abc")
	assert.Equal(t, got.stopHookActive, false)
}

func TestDetectHookParsesStopHookActive(t *testing.T) {
	payload := `{"session_id":"sess-xyz","stop_hook_active":true}`
	got := detectHook(strings.NewReader(payload))
	assert.Assert(t, got != nil)
	assert.Equal(t, got.sessionID, "sess-xyz")
	assert.Equal(t, got.stopHookActive, true)
}

func TestDetectHookNilWhenNoSessionID(t *testing.T) {
	payload := `{"stop_hook_active":true}`
	got := detectHook(strings.NewReader(payload))
	assert.Assert(t, got == nil, "missing session_id should return nil")
}

func TestDetectHookNilOnEmptyInput(t *testing.T) {
	got := detectHook(strings.NewReader(""))
	assert.Assert(t, got == nil)
}

func TestDetectHookNilOnInvalidJSON(t *testing.T) {
	got := detectHook(strings.NewReader("not json"))
	assert.Assert(t, got == nil)
}

func TestHasRemoteCommands(t *testing.T) {
	cfg := &config.ProjectConfig{Commands: []config.Command{
		{Name: "lint", Run: "golangci-lint run"},
		{Name: "test", Run: "go test ./...", Remote: true},
	}}

	assert.Assert(t, hasRemoteCommands(cfg, ""), "should detect remote command in full list")
	assert.Assert(t, hasRemoteCommands(cfg, "test"), "named remote command should return true")
	assert.Assert(t, !hasRemoteCommands(cfg, "lint"), "named local command should return false")

	localOnly := &config.ProjectConfig{Commands: []config.Command{
		{Name: "lint", Run: "golangci-lint run"},
	}}
	assert.Assert(t, !hasRemoteCommands(localOnly, ""), "no remote commands should return false")
}

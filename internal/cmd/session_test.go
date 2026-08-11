package cmd

import (
	"bytes"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
)

func TestSessionStart_SavesSessionID(t *testing.T) {
	dir := initGitRepo(t)
	t.Setenv(config.EnvXDGDataHome, t.TempDir())

	root := NewRootCmd("test")
	root.SetArgs([]string{"session", "start", "--project", dir})
	root.SetIn(strings.NewReader(`{"session_id":"sess-test","hook_event_name":"SessionStart"}`))
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	assert.NilError(t, root.Execute())
	assert.Equal(t, sidecar.LoadSessionID(dir), "sess-test")
}

func TestSessionStart_NoopOnEmptyPayload(t *testing.T) {
	dir := initGitRepo(t)
	t.Setenv(config.EnvXDGDataHome, t.TempDir())

	root := NewRootCmd("test")
	root.SetArgs([]string{"session", "start", "--project", dir})
	root.SetIn(strings.NewReader(`{}`))
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	assert.NilError(t, root.Execute())
	assert.Equal(t, sidecar.LoadSessionID(dir), "")
}

func TestSessionStart_NoopOnInvalidJSON(t *testing.T) {
	dir := initGitRepo(t)
	t.Setenv(config.EnvXDGDataHome, t.TempDir())

	root := NewRootCmd("test")
	root.SetArgs([]string{"session", "start", "--project", dir})
	root.SetIn(strings.NewReader(`not json`))
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	assert.NilError(t, root.Execute())
	assert.Equal(t, sidecar.LoadSessionID(dir), "")
}

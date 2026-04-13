package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
)

func fakeConfirmYes(_ string, _ bool) (bool, error) { return true, nil }
func fakeConfirmNo(_ string, _ bool) (bool, error)  { return false, nil }
func fakeConfirmErr(_ string, _ bool) (bool, error) {
	return false, errors.New("no tty")
}

func testStreams() (iostream.Streams, *bytes.Buffer, *bytes.Buffer) {
	var out, errOut bytes.Buffer
	return iostream.Streams{Out: &out, Err: &errOut}, &out, &errOut
}

func TestWriteSettingsNewFile(t *testing.T) {
	dir := t.TempDir()
	streams, _, errOut := testStreams()

	commands := []config.Command{
		{Name: "test", Run: "go test ./...", Timeout: 60},
	}

	err := writeSettings(dir, commands, streams, fakeConfirmYes)
	assert.NilError(t, err)

	data, err := os.ReadFile(filepath.Join(dir, ".claude", "settings.json"))
	assert.NilError(t, err)

	var parsed map[string]interface{}
	assert.NilError(t, json.Unmarshal(data, &parsed))
	assert.Equal(t, parsed["$schema"], "https://json.schemastore.org/claude-code-settings.json")
	assert.Assert(t, errOut.Len() > 0)
}

func TestWriteSettingsExistingMergeApplied(t *testing.T) {
	dir := t.TempDir()
	claudeDir := filepath.Join(dir, ".claude")
	assert.NilError(t, os.MkdirAll(claudeDir, 0o755))

	existing := []byte(`{
  "env": {"CHUNK_HOOK_ENABLE": "1"},
  "permissions": {"allow": ["Read"]}
}`)
	assert.NilError(t, os.WriteFile(filepath.Join(claudeDir, "settings.json"), existing, 0o644))

	streams, _, errOut := testStreams()
	commands := []config.Command{
		{Name: "test", Run: "go test ./...", Timeout: 60},
	}

	err := writeSettings(dir, commands, streams, fakeConfirmYes)
	assert.NilError(t, err)

	// settings.json should be updated with merged content.
	data, err := os.ReadFile(filepath.Join(claudeDir, "settings.json"))
	assert.NilError(t, err)

	var merged map[string]interface{}
	assert.NilError(t, json.Unmarshal(data, &merged))

	// Existing key preserved.
	env := merged["env"].(map[string]interface{})
	assert.Equal(t, env["CHUNK_HOOK_ENABLE"], "1")

	// Permissions unioned.
	perms := merged["permissions"].(map[string]interface{})
	allow := perms["allow"].([]interface{})
	allowStrs := make([]string, len(allow))
	for i, v := range allow {
		allowStrs[i] = v.(string)
	}
	assert.Assert(t, contains(allowStrs, "Read"))
	assert.Assert(t, contains(allowStrs, "Bash(chunk:*)"))

	// Hooks added.
	assert.Assert(t, merged["hooks"] != nil)

	// No example file written.
	_, statErr := os.Stat(filepath.Join(claudeDir, "settings.example.json"))
	assert.Assert(t, os.IsNotExist(statErr))

	assert.Assert(t, bytes.Contains(errOut.Bytes(), []byte("Updated")))
}

func TestWriteSettingsExistingMergeDeclined(t *testing.T) {
	dir := t.TempDir()
	claudeDir := filepath.Join(dir, ".claude")
	assert.NilError(t, os.MkdirAll(claudeDir, 0o755))

	existing := []byte(`{"permissions": {"allow": ["Read"]}}`)
	assert.NilError(t, os.WriteFile(filepath.Join(claudeDir, "settings.json"), existing, 0o644))

	streams, _, errOut := testStreams()
	commands := []config.Command{
		{Name: "test", Run: "go test ./...", Timeout: 60},
	}

	err := writeSettings(dir, commands, streams, fakeConfirmNo)
	assert.NilError(t, err)

	// Original settings.json untouched.
	data, err := os.ReadFile(filepath.Join(claudeDir, "settings.json"))
	assert.NilError(t, err)
	assert.Equal(t, string(data), string(existing))

	// Example file written.
	_, statErr := os.Stat(filepath.Join(claudeDir, "settings.example.json"))
	assert.NilError(t, statErr)

	assert.Assert(t, bytes.Contains(errOut.Bytes(), []byte("settings.example.json")))
}

func TestWriteSettingsExistingNoTTYFallback(t *testing.T) {
	dir := t.TempDir()
	claudeDir := filepath.Join(dir, ".claude")
	assert.NilError(t, os.MkdirAll(claudeDir, 0o755))

	existing := []byte(`{"permissions": {"allow": ["Read"]}}`)
	assert.NilError(t, os.WriteFile(filepath.Join(claudeDir, "settings.json"), existing, 0o644))

	streams, _, _ := testStreams()
	commands := []config.Command{
		{Name: "test", Run: "go test ./...", Timeout: 60},
	}

	// Simulates tui.ErrNoTTY — confirm returns an error.
	err := writeSettings(dir, commands, streams, fakeConfirmErr)
	assert.NilError(t, err)

	// Original untouched, example written.
	data, err := os.ReadFile(filepath.Join(claudeDir, "settings.json"))
	assert.NilError(t, err)
	assert.Equal(t, string(data), string(existing))

	_, statErr := os.Stat(filepath.Join(claudeDir, "settings.example.json"))
	assert.NilError(t, statErr)
}

func TestWriteSettingsAlreadyUpToDate(t *testing.T) {
	dir := t.TempDir()
	claudeDir := filepath.Join(dir, ".claude")
	assert.NilError(t, os.MkdirAll(claudeDir, 0o755))

	commands := []config.Command{
		{Name: "test", Run: "go test ./...", Timeout: 60},
	}

	// First write — creates settings.json.
	streams1, _, _ := testStreams()
	assert.NilError(t, writeSettings(dir, commands, streams1, fakeConfirmYes))

	// Second write with same commands — should be up to date.
	streams2, _, errOut := testStreams()
	assert.NilError(t, writeSettings(dir, commands, streams2, fakeConfirmYes))
	assert.Assert(t, bytes.Contains(errOut.Bytes(), []byte("already up to date")))
}

func contains(ss []string, target string) bool {
	for _, s := range ss {
		if s == target {
			return true
		}
	}
	return false
}

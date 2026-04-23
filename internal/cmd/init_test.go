package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
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
  "env": {"MY_CUSTOM_VAR": "hello"},
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

	// Existing env key preserved.
	env := merged["env"].(map[string]interface{})
	assert.Equal(t, env["MY_CUSTOM_VAR"], "hello")

	// Permissions unioned.
	perms := merged["permissions"].(map[string]interface{})
	allow := perms["allow"].([]interface{})
	allowStrs := make([]string, len(allow))
	for i, v := range allow {
		allowStrs[i] = v.(string)
	}
	assert.Assert(t, slices.Contains(allowStrs, "Read"))
	assert.Assert(t, slices.Contains(allowStrs, "Bash(chunk:*)"))

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

func setupShellEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	t.Setenv(config.EnvShell, "/bin/zsh")
	return home
}

func TestCompletionInstalledFalseWhenNoRCFile(t *testing.T) {
	setupShellEnv(t)

	installed, err := completionInstalled()
	assert.NilError(t, err)
	assert.Assert(t, !installed)
}

func TestCompletionInstalledTrueWhenTagPresent(t *testing.T) {
	home := setupShellEnv(t)
	rcFile := filepath.Join(home, ".zshrc")
	assert.NilError(t, os.WriteFile(rcFile, []byte(completionTag+"\nsource <(chunk completion zsh)\n"), 0o644))

	installed, err := completionInstalled()
	assert.NilError(t, err)
	assert.Assert(t, installed)
}

func TestCompletionInstalledUnsupportedShell(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	t.Setenv(config.EnvShell, "/bin/fish")

	_, err := completionInstalled()
	assert.Assert(t, err != nil)
}

func TestInstallCompletionWritesRCFile(t *testing.T) {
	home := setupShellEnv(t)
	rcFile := filepath.Join(home, ".zshrc")
	streams, _, errOut := testStreams()

	err := installCompletion(streams)
	assert.NilError(t, err)

	data, err := os.ReadFile(rcFile)
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(string(data), completionTag))
	assert.Assert(t, strings.Contains(string(data), "source <(chunk completion zsh)"))
	assert.Assert(t, bytes.Contains(errOut.Bytes(), []byte(ui.Success("Completion installed."))))
}

func TestInstallCompletionSkipsWhenAlreadyInstalled(t *testing.T) {
	home := setupShellEnv(t)
	rcFile := filepath.Join(home, ".zshrc")
	existing := completionTag + "\nsource <(chunk completion zsh)\n"
	assert.NilError(t, os.WriteFile(rcFile, []byte(existing), 0o644))

	streams, _, errOut := testStreams()
	err := installCompletion(streams)
	assert.NilError(t, err)

	// File unchanged.
	data, err := os.ReadFile(rcFile)
	assert.NilError(t, err)
	assert.Equal(t, string(data), existing)
	assert.Assert(t, bytes.Contains(errOut.Bytes(), []byte("already installed")))
}

func TestEnsureGitignoreEntriesCreatesNew(t *testing.T) {
	dir := t.TempDir()
	streams, _, _ := testStreams()

	err := ensureGitignoreEntries(dir, streams)
	assert.NilError(t, err)

	data, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	assert.NilError(t, err)
	for _, entry := range sandboxGitignoreEntries {
		assert.Assert(t, strings.Contains(string(data), entry), "missing %s", entry)
	}
}

func TestEnsureGitignoreEntriesAppendsToExisting(t *testing.T) {
	dir := t.TempDir()
	existing := "node_modules/\n.env\n"
	assert.NilError(t, os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(existing), 0o644))

	streams, _, _ := testStreams()
	err := ensureGitignoreEntries(dir, streams)
	assert.NilError(t, err)

	data, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	assert.NilError(t, err)
	content := string(data)
	assert.Assert(t, strings.HasPrefix(content, existing))
	for _, entry := range sandboxGitignoreEntries {
		assert.Assert(t, strings.Contains(content, entry), "missing %s", entry)
	}
}

func TestEnsureGitignoreEntriesIdempotent(t *testing.T) {
	dir := t.TempDir()
	streams, _, _ := testStreams()

	assert.NilError(t, ensureGitignoreEntries(dir, streams))
	first, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	assert.NilError(t, err)

	assert.NilError(t, ensureGitignoreEntries(dir, streams))
	second, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	assert.NilError(t, err)

	assert.Equal(t, string(first), string(second))
}

func TestInstallCompletionBashWritesRCFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	t.Setenv(config.EnvShell, "/bin/bash")

	// Create .bashrc so detectShell prefers it over .bash_profile.
	rcFile := filepath.Join(home, ".bashrc")
	assert.NilError(t, os.WriteFile(rcFile, []byte("# existing config\n"), 0o644))

	streams, _, errOut := testStreams()
	err := installCompletion(streams)
	assert.NilError(t, err)

	data, err := os.ReadFile(rcFile)
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(string(data), completionTag))
	assert.Assert(t, strings.Contains(string(data), "source <(chunk completion bash)"))
	assert.Assert(t, bytes.Contains(errOut.Bytes(), []byte(ui.Success("Completion installed."))))
}

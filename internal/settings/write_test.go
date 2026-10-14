package settings

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
)

func fakeConfirmYes(_, _ string) (bool, error) { return true, nil }
func fakeConfirmNo(_, _ string) (bool, error)  { return false, nil }
func fakeConfirmErr(_, _ string) (bool, error) {
	return false, errors.New("no tty")
}

// mergedHookCommands flattens the hook entry commands across every group of one
// hook type, so a test can assert on what actually runs regardless of grouping.
func mergedHookCommands(groups interface{}) []string {
	list, _ := groups.([]interface{})
	var commands []string
	for _, g := range list {
		group, ok := g.(map[string]interface{})
		if !ok {
			continue
		}
		entries, _ := group["hooks"].([]interface{})
		for _, e := range entries {
			entry, ok := e.(map[string]interface{})
			if !ok {
				continue
			}
			if cmd, ok := entry["command"].(string); ok {
				commands = append(commands, cmd)
			}
		}
	}
	return commands
}

func TestWriteClaudeNewFile(t *testing.T) {
	dir := t.TempDir()

	commands := []config.Command{
		{Name: "test", Run: "go test ./...", Timeout: 60},
	}

	res, err := WriteClaude(dir, commands, fakeConfirmYes)
	assert.NilError(t, err)
	assert.Equal(t, res.Outcome, Created)
	assert.Equal(t, res.Path, ".claude/settings.json")

	data, err := os.ReadFile(filepath.Join(dir, ".claude", "settings.json"))
	assert.NilError(t, err)

	var parsed map[string]interface{}
	assert.NilError(t, json.Unmarshal(data, &parsed))
	assert.Equal(t, parsed["$schema"], "https://json.schemastore.org/claude-code-settings.json")
}

func TestWriteClaudeExistingMergeApplied(t *testing.T) {
	dir := t.TempDir()
	claudeDir := filepath.Join(dir, ".claude")
	assert.NilError(t, os.MkdirAll(claudeDir, 0o755))

	existing := []byte(`{
  "env": {"MY_CUSTOM_VAR": "hello"},
  "permissions": {"allow": ["Read"]}
}`)
	assert.NilError(t, os.WriteFile(filepath.Join(claudeDir, "settings.json"), existing, 0o644))

	commands := []config.Command{
		{Name: "test", Run: "go test ./...", Timeout: 60},
	}

	res, err := WriteClaude(dir, commands, fakeConfirmYes)
	assert.NilError(t, err)
	assert.Equal(t, res.Outcome, Updated)

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

	// Hooks added — both kinds. "hooks != nil" would pass with only the commit
	// hooks merged in, which is exactly the state that left repos looking
	// configured while nothing validated at session end.
	hooks, ok := merged["hooks"].(map[string]interface{})
	assert.Assert(t, ok, "expected a hooks object, got: %s", data)

	commitCommands := mergedHookCommands(hooks["PreToolUse"])
	assert.Assert(t, slices.ContainsFunc(commitCommands, func(c string) bool {
		return strings.HasSuffix(c, "chunk validate")
	}), "expected the commit hook to call chunk validate, got: %v", commitCommands)

	stopCommands := mergedHookCommands(hooks["Stop"])
	assert.DeepEqual(t, stopCommands, []string{StopCommand})

	// No example file written.
	_, statErr := os.Stat(filepath.Join(claudeDir, "settings.example.json"))
	assert.Assert(t, os.IsNotExist(statErr))
}

func TestWriteClaudeExistingMergeDeclined(t *testing.T) {
	dir := t.TempDir()
	claudeDir := filepath.Join(dir, ".claude")
	assert.NilError(t, os.MkdirAll(claudeDir, 0o755))

	existing := []byte(`{"permissions": {"allow": ["Read"]}}`)
	assert.NilError(t, os.WriteFile(filepath.Join(claudeDir, "settings.json"), existing, 0o644))

	commands := []config.Command{
		{Name: "test", Run: "go test ./...", Timeout: 60},
	}

	res, err := WriteClaude(dir, commands, fakeConfirmNo)
	assert.NilError(t, err)
	assert.Equal(t, res.Outcome, Declined)

	// Original settings.json untouched.
	data, err := os.ReadFile(filepath.Join(claudeDir, "settings.json"))
	assert.NilError(t, err)
	assert.Equal(t, string(data), string(existing))

	// No example file written on explicit decline.
	_, statErr := os.Stat(filepath.Join(claudeDir, "settings.example.json"))
	assert.Assert(t, errors.Is(statErr, fs.ErrNotExist))
}

func TestWriteClaudeExistingNoTTYFallback(t *testing.T) {
	dir := t.TempDir()
	claudeDir := filepath.Join(dir, ".claude")
	assert.NilError(t, os.MkdirAll(claudeDir, 0o755))

	existing := []byte(`{"permissions": {"allow": ["Read"]}}`)
	assert.NilError(t, os.WriteFile(filepath.Join(claudeDir, "settings.json"), existing, 0o644))

	commands := []config.Command{
		{Name: "test", Run: "go test ./...", Timeout: 60},
	}

	// Simulates ui.ErrNoTTY — confirm returns an error.
	res, err := WriteClaude(dir, commands, fakeConfirmErr)
	assert.NilError(t, err)
	assert.Equal(t, res.Outcome, WroteExample)
	assert.Equal(t, res.Example, ".claude/settings.example.json")
	assert.ErrorContains(t, res.ConfirmErr, "no tty")

	// Original untouched, example written.
	data, err := os.ReadFile(filepath.Join(claudeDir, "settings.json"))
	assert.NilError(t, err)
	assert.Equal(t, string(data), string(existing))

	_, statErr := os.Stat(filepath.Join(claudeDir, "settings.example.json"))
	assert.NilError(t, statErr)
}

func TestWriteClaudeAlreadyUpToDate(t *testing.T) {
	dir := t.TempDir()
	claudeDir := filepath.Join(dir, ".claude")
	assert.NilError(t, os.MkdirAll(claudeDir, 0o755))

	commands := []config.Command{
		{Name: "test", Run: "go test ./...", Timeout: 60},
	}

	// First write — creates settings.json.
	_, err := WriteClaude(dir, commands, fakeConfirmYes)
	assert.NilError(t, err)

	// Second write with same commands — should be up to date.
	res, err := WriteClaude(dir, commands, fakeConfirmYes)
	assert.NilError(t, err)
	assert.Equal(t, res.Outcome, UpToDate)
}

func TestWriteCodexNewFile(t *testing.T) {
	dir := t.TempDir()

	commands := []config.Command{
		{Name: "test", Run: "go test ./...", Timeout: 60},
	}

	res, err := WriteCodex(dir, commands, fakeConfirmYes)
	assert.NilError(t, err)
	assert.Equal(t, res.Outcome, Created)
	assert.Equal(t, res.Path, ".codex/hooks.json")

	data, err := os.ReadFile(filepath.Join(dir, ".codex", "hooks.json"))
	assert.NilError(t, err)

	var parsed map[string]interface{}
	assert.NilError(t, json.Unmarshal(data, &parsed))
	// Must not contain Claude Code-specific keys.
	assert.Assert(t, parsed["$schema"] == nil)
	assert.Assert(t, parsed["permissions"] == nil)
	assert.Assert(t, parsed["hooks"] != nil)
}

func TestWriteCodexExistingMergeApplied(t *testing.T) {
	dir := t.TempDir()
	codexDir := filepath.Join(dir, ".codex")
	assert.NilError(t, os.MkdirAll(codexDir, 0o755))

	existing := []byte(`{
  "hooks": {
    "PostToolUse": [{"matcher": "*", "hooks": [{"type": "command", "command": "audit", "timeout": 10}]}]
  }
}`)
	assert.NilError(t, os.WriteFile(filepath.Join(codexDir, "hooks.json"), existing, 0o644))

	commands := []config.Command{
		{Name: "test", Run: "go test ./...", Timeout: 60},
	}

	res, err := WriteCodex(dir, commands, fakeConfirmYes)
	assert.NilError(t, err)
	assert.Equal(t, res.Outcome, Updated)

	data, err := os.ReadFile(filepath.Join(codexDir, "hooks.json"))
	assert.NilError(t, err)

	var merged map[string]interface{}
	assert.NilError(t, json.Unmarshal(data, &merged))

	hooks, ok := merged["hooks"].(map[string]interface{})
	assert.Assert(t, ok, "expected hooks to be a map")
	// Existing PostToolUse preserved.
	assert.Assert(t, hooks["PostToolUse"] != nil)
	// New PreToolUse and Stop hooks added.
	assert.Assert(t, hooks["PreToolUse"] != nil)
	assert.Assert(t, hooks["Stop"] != nil)
}

func TestWriteCodexExistingMergeDeclined(t *testing.T) {
	dir := t.TempDir()
	codexDir := filepath.Join(dir, ".codex")
	assert.NilError(t, os.MkdirAll(codexDir, 0o755))

	existing := []byte(`{"hooks": {}}`)
	assert.NilError(t, os.WriteFile(filepath.Join(codexDir, "hooks.json"), existing, 0o644))

	commands := []config.Command{
		{Name: "test", Run: "go test ./...", Timeout: 60},
	}

	res, err := WriteCodex(dir, commands, fakeConfirmNo)
	assert.NilError(t, err)
	assert.Equal(t, res.Outcome, Declined)

	// Original hooks.json untouched.
	data, err := os.ReadFile(filepath.Join(codexDir, "hooks.json"))
	assert.NilError(t, err)
	assert.Equal(t, string(data), string(existing))

	// No example file written on explicit decline.
	_, statErr := os.Stat(filepath.Join(codexDir, "hooks.example.json"))
	assert.Assert(t, errors.Is(statErr, fs.ErrNotExist))
}

func TestWriteCodexAlreadyUpToDate(t *testing.T) {
	dir := t.TempDir()
	commands := []config.Command{
		{Name: "test", Run: "go test ./...", Timeout: 60},
	}

	_, err := WriteCodex(dir, commands, fakeConfirmYes)
	assert.NilError(t, err)

	res, err := WriteCodex(dir, commands, fakeConfirmYes)
	assert.NilError(t, err)
	assert.Equal(t, res.Outcome, UpToDate)
}

func TestCodexWanted(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no codex on PATH

	t.Run("neither installed nor configured", func(t *testing.T) {
		assert.Assert(t, !CodexWanted(t.TempDir(), t.TempDir()))
	})
	t.Run("installed", func(t *testing.T) {
		home := t.TempDir()
		assert.NilError(t, os.Mkdir(filepath.Join(home, ".codex"), 0o755))
		assert.Assert(t, CodexWanted(t.TempDir(), home))
	})
	t.Run("configured in the project", func(t *testing.T) {
		dir := t.TempDir()
		assert.NilError(t, os.Mkdir(filepath.Join(dir, ".codex"), 0o755))
		assert.Assert(t, CodexWanted(dir, ""))
	})
}

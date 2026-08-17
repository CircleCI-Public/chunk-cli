package settings

import (
	"encoding/json"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
)

func TestBuildHookTimeoutDefaultsToSixty(t *testing.T) {
	// A command with Timeout: 0 must produce a non-zero timeout in the generated
	// hook entry — the default of 60s must be applied. MUT-006 caught this gap by
	// changing the default to 0, which causes Claude Code to treat the hook as
	// having no timeout limit.
	cmds := []config.Command{
		{Name: "test", Run: "go test ./...", Timeout: 0},
	}
	data, err := Build(cmds)
	assert.NilError(t, err)

	var s map[string]interface{}
	assert.NilError(t, json.Unmarshal(data, &s))

	hooks := s["hooks"].(map[string]interface{})
	preToolUse := hooks["PreToolUse"].([]interface{})
	group := preToolUse[0].(map[string]interface{})
	assert.Equal(t, group["matcher"], "Bash", "group matcher must be tool name only")
	entries := group["hooks"].([]interface{})
	entry := entries[0].(map[string]interface{})
	assert.Equal(t, entry["if"], CommitIfFilter, "entry must carry if filter for git commit")

	timeout, _ := entry["timeout"].(float64)
	assert.Assert(t, timeout == 60, "expected default hook timeout of 60 for command with Timeout: 0, got: %v", timeout)
}

func TestBuildHookMatcherIsToolName(t *testing.T) {
	// The group matcher must be the bare tool name "Bash" so Claude Code's
	// hook dispatcher matches it as an exact string. "Bash(git commit*)" was
	// previously used but is treated as a JS regex and never fires.
	cmds := []config.Command{
		{Name: "test", Run: "task test", Timeout: 60},
	}
	data, err := Build(cmds)
	assert.NilError(t, err)

	var s map[string]interface{}
	assert.NilError(t, json.Unmarshal(data, &s))

	hooks := s["hooks"].(map[string]interface{})
	preToolUse := hooks["PreToolUse"].([]interface{})
	group := preToolUse[0].(map[string]interface{})
	assert.Equal(t, group["matcher"], CommitMatcher)

	entries := group["hooks"].([]interface{})
	entry := entries[0].(map[string]interface{})
	assert.Equal(t, entry["if"], CommitIfFilter)
}

func TestBuildHookTimeoutRespectsExplicitValue(t *testing.T) {
	cmds := []config.Command{
		{Name: "lint", Run: "golangci-lint run", Timeout: 120},
	}
	data, err := Build(cmds)
	assert.NilError(t, err)

	var s map[string]interface{}
	assert.NilError(t, json.Unmarshal(data, &s))

	hooks := s["hooks"].(map[string]interface{})
	preToolUse := hooks["PreToolUse"].([]interface{})
	group := preToolUse[0].(map[string]interface{})
	entries := group["hooks"].([]interface{})
	entry := entries[0].(map[string]interface{})

	timeout, _ := entry["timeout"].(float64)
	assert.Assert(t, timeout == 120, "expected explicit timeout of 120, got: %v", timeout)
}

func TestBuildIncludesWriteFileGuard(t *testing.T) {
	cmds := []config.Command{
		{Name: "test", Run: "task test", Timeout: 60},
	}
	data, err := Build(cmds)
	assert.NilError(t, err)

	var s map[string]interface{}
	assert.NilError(t, json.Unmarshal(data, &s))

	hooks := s["hooks"].(map[string]interface{})
	preToolUse := hooks["PreToolUse"].([]interface{})

	// Must have at least two groups: Bash (commit checks) and Write (file guard).
	assert.Assert(t, len(preToolUse) >= 2, "expected at least 2 PreToolUse groups, got %d", len(preToolUse))

	var writeGroup map[string]interface{}
	for _, g := range preToolUse {
		group := g.(map[string]interface{})
		if group["matcher"] == WriteFileMatcher {
			writeGroup = group
			break
		}
	}
	assert.Assert(t, writeGroup != nil, "expected a PreToolUse group with matcher %q", WriteFileMatcher)

	entries := writeGroup["hooks"].([]interface{})
	assert.Assert(t, len(entries) > 0, "Write hook group must have at least one entry")

	entry := entries[0].(map[string]interface{})
	cmd, _ := entry["command"].(string)
	assert.Assert(t, cmd != "", "Write hook entry must have a non-empty command")

	// Command must reference the expected blocklist filenames.
	for _, name := range []string{"CODEX.md", "GEMINI.md", ".cursorrules", "WINDSURF.md"} {
		assert.Assert(t, strings.Contains(cmd, name), "Write guard command must reference %q", name)
	}
}

func TestBuildCodexNoMetadata(t *testing.T) {
	cmds := []config.Command{
		{Name: "test", Run: "go test ./...", Timeout: 60},
	}
	data, err := BuildCodex(cmds)
	assert.NilError(t, err)

	var s map[string]interface{}
	assert.NilError(t, json.Unmarshal(data, &s))

	// Must not contain Claude Code-specific keys.
	_, hasSchema := s["$schema"]
	assert.Assert(t, !hasSchema, "BuildCodex must not include $schema")
	_, hasComment := s["_comment"]
	assert.Assert(t, !hasComment, "BuildCodex must not include _comment")
	_, hasPerms := s["permissions"]
	assert.Assert(t, !hasPerms, "BuildCodex must not include permissions")
}

func TestBuildCodexCommandNotWrappedWithCd(t *testing.T) {
	cmds := []config.Command{
		{Name: "test", Run: "go test ./...", Timeout: 60},
	}
	data, err := BuildCodex(cmds)
	assert.NilError(t, err)

	var s map[string]interface{}
	assert.NilError(t, json.Unmarshal(data, &s))

	hooks, ok := s["hooks"].(map[string]interface{})
	assert.Assert(t, ok, "expected hooks map")
	preToolUse, ok := hooks["PreToolUse"].([]interface{})
	assert.Assert(t, ok && len(preToolUse) > 0, "expected PreToolUse array")
	group, ok := preToolUse[0].(map[string]interface{})
	assert.Assert(t, ok, "expected hook group to be a map")
	entries, ok := group["hooks"].([]interface{})
	assert.Assert(t, ok && len(entries) > 0, "expected hook entries")
	entry, ok := entries[0].(map[string]interface{})
	assert.Assert(t, ok, "expected hook entry to be a map")

	cmd, _ := entry["command"].(string)
	assert.Equal(t, cmd, "go test ./...", "Codex hook command must be the raw command without a cd prefix")
}

func TestBuildCodexTimeoutDefaultsToSixty(t *testing.T) {
	cmds := []config.Command{
		{Name: "test", Run: "go test ./...", Timeout: 0},
	}
	data, err := BuildCodex(cmds)
	assert.NilError(t, err)

	var s map[string]interface{}
	assert.NilError(t, json.Unmarshal(data, &s))

	hooks, ok := s["hooks"].(map[string]interface{})
	assert.Assert(t, ok, "expected hooks map")
	preToolUse, ok := hooks["PreToolUse"].([]interface{})
	assert.Assert(t, ok && len(preToolUse) > 0, "expected PreToolUse array")
	group, ok := preToolUse[0].(map[string]interface{})
	assert.Assert(t, ok, "expected hook group to be a map")
	entries, ok := group["hooks"].([]interface{})
	assert.Assert(t, ok && len(entries) > 0, "expected hook entries")
	entry, ok := entries[0].(map[string]interface{})
	assert.Assert(t, ok, "expected hook entry to be a map")

	timeout, _ := entry["timeout"].(float64)
	assert.Assert(t, timeout == 60, "expected default hook timeout of 60, got: %v", timeout)
}

func TestBuildCodexIncludesStopHook(t *testing.T) {
	cmds := []config.Command{
		{Name: "test", Run: "go test ./...", Timeout: 60},
	}
	data, err := BuildCodex(cmds)
	assert.NilError(t, err)

	var s map[string]interface{}
	assert.NilError(t, json.Unmarshal(data, &s))

	hooks := s["hooks"].(map[string]interface{})
	stop, ok := hooks["Stop"].([]interface{})
	assert.Assert(t, ok && len(stop) > 0, "BuildCodex must include a Stop hook")

	group, ok := stop[0].(map[string]interface{})
	assert.Assert(t, ok, "expected stop group to be a map")
	entries, ok := group["hooks"].([]interface{})
	assert.Assert(t, ok && len(entries) > 0, "expected stop hook entries")
	entry, ok := entries[0].(map[string]interface{})
	assert.Assert(t, ok, "expected stop hook entry to be a map")
	assert.Equal(t, entry["command"], "chunk validate")
}

func TestBuildCodexNoCommandsProducesEmptyHooks(t *testing.T) {
	data, err := BuildCodex(nil)
	assert.NilError(t, err)

	var s map[string]interface{}
	assert.NilError(t, json.Unmarshal(data, &s))

	_, hasHooks := s["hooks"]
	assert.Assert(t, !hasHooks, "BuildCodex with no commands must produce empty hooks")
}

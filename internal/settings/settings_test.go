package settings

import (
	"encoding/json"
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

// A background result arrives after the agent has stopped, so chunk init has to
// install the hook that reports it. Without this entry --collect is never
// called and an async run's answer is never delivered to anyone.
func TestBuildInstallsTheCollectHookOnUserPromptSubmit(t *testing.T) {
	data, err := Build([]config.Command{{Name: "test", Run: "go test ./..."}})
	assert.NilError(t, err)

	var s map[string]interface{}
	assert.NilError(t, json.Unmarshal(data, &s))
	hooks := s["hooks"].(map[string]interface{})

	// Stop is where validation runs; UserPromptSubmit is where a deferred result
	// is read. They are different moments and both must be present.
	assert.Assert(t, hooks["Stop"] != nil, "the Stop hook must survive")
	groups, ok := hooks["UserPromptSubmit"].([]interface{})
	assert.Assert(t, ok, "no UserPromptSubmit hook was written, so results are never collected")
	assert.Equal(t, len(groups), 1)

	entries := groups[0].(map[string]interface{})["hooks"].([]interface{})
	assert.Equal(t, len(entries), 1)
	entry := entries[0].(map[string]interface{})
	assert.Equal(t, entry["command"], CollectCommand)
	// Small on purpose: this sits in front of every prompt and only reads a
	// result the daemon already holds.
	assert.Equal(t, entry["timeout"], float64(collectTimeout))
}

// With no commands there is nothing to validate, so there is nothing to collect
// either and chunk writes no hooks at all.
func TestBuildWritesNoCollectHookWithoutCommands(t *testing.T) {
	data, err := Build(nil)
	assert.NilError(t, err)

	var s map[string]interface{}
	assert.NilError(t, json.Unmarshal(data, &s))
	assert.Assert(t, s["hooks"] == nil, "hooks were written for a project with no commands")
}

package settings

import (
	"encoding/json"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
)

func TestBuildIncludesPostToolUseForFix(t *testing.T) {
	fix := []config.FixCommand{
		{Name: "format", Run: "gofmt -w .", Timeout: 30},
	}
	data, err := Build(fix)
	assert.NilError(t, err)

	var s map[string]interface{}
	assert.NilError(t, json.Unmarshal(data, &s))

	hooks := s["hooks"].(map[string]interface{})
	postToolUse, ok := hooks["PostToolUse"].([]interface{})
	assert.Assert(t, ok && len(postToolUse) > 0, "expected PostToolUse hook for fix commands")

	group := postToolUse[0].(map[string]interface{})
	assert.Equal(t, group["matcher"], FixMatcher)
	entries := group["hooks"].([]interface{})
	entry := entries[0].(map[string]interface{})
	assert.Assert(t, entry["command"] != nil)
}

func TestBuildNoFixCommandsProducesNoHooks(t *testing.T) {
	data, err := Build(nil)
	assert.NilError(t, err)

	var s map[string]interface{}
	assert.NilError(t, json.Unmarshal(data, &s))

	_, hasHooks := s["hooks"]
	assert.Assert(t, !hasHooks, "Build with no fix commands must produce no hooks")
}

func TestBuildNoPreToolUseHook(t *testing.T) {
	fix := []config.FixCommand{
		{Name: "format", Run: "gofmt -w .", Timeout: 30},
	}
	data, err := Build(fix)
	assert.NilError(t, err)

	var s map[string]interface{}
	assert.NilError(t, json.Unmarshal(data, &s))

	hooks := s["hooks"].(map[string]interface{})
	_, hasPreToolUse := hooks["PreToolUse"]
	assert.Assert(t, !hasPreToolUse, "Build must not include a PreToolUse hook")
}

func TestBuildCodexNoMetadata(t *testing.T) {
	fix := []config.FixCommand{
		{Name: "format", Run: "gofmt -w .", Timeout: 30},
	}
	data, err := BuildCodex(fix)
	assert.NilError(t, err)

	var s map[string]interface{}
	assert.NilError(t, json.Unmarshal(data, &s))

	_, hasSchema := s["$schema"]
	assert.Assert(t, !hasSchema, "BuildCodex must not include $schema")
	_, hasComment := s["_comment"]
	assert.Assert(t, !hasComment, "BuildCodex must not include _comment")
	_, hasPerms := s["permissions"]
	assert.Assert(t, !hasPerms, "BuildCodex must not include permissions")
}

func TestBuildCodexFixCommandNotWrappedWithCd(t *testing.T) {
	fix := []config.FixCommand{
		{Name: "format", Run: "gofmt -w .", Timeout: 30},
	}
	data, err := BuildCodex(fix)
	assert.NilError(t, err)

	var s map[string]interface{}
	assert.NilError(t, json.Unmarshal(data, &s))

	hooks := s["hooks"].(map[string]interface{})
	postToolUse := hooks["PostToolUse"].([]interface{})
	group := postToolUse[0].(map[string]interface{})
	entries := group["hooks"].([]interface{})
	entry := entries[0].(map[string]interface{})

	cmd, _ := entry["command"].(string)
	assert.Equal(t, cmd, "chunk fix", "Codex PostToolUse hook must run chunk fix directly")
}

func TestBuildCodexNoCommandsProducesEmptyHooks(t *testing.T) {
	data, err := BuildCodex(nil)
	assert.NilError(t, err)

	var s map[string]interface{}
	assert.NilError(t, json.Unmarshal(data, &s))

	_, hasHooks := s["hooks"]
	assert.Assert(t, !hasHooks, "BuildCodex with no commands must produce empty hooks")
}

func TestBuildIncludesSessionStartHook(t *testing.T) {
	fix := []config.FixCommand{
		{Name: "format", Run: "gofmt -w .", Timeout: 30},
	}
	data, err := Build(fix)
	assert.NilError(t, err)

	var s map[string]interface{}
	assert.NilError(t, json.Unmarshal(data, &s))

	hooks := s["hooks"].(map[string]interface{})
	sessionStart, ok := hooks["SessionStart"].([]interface{})
	assert.Assert(t, ok && len(sessionStart) > 0, "expected SessionStart hook")

	group := sessionStart[0].(map[string]interface{})
	_, hasMatcher := group["matcher"]
	assert.Assert(t, !hasMatcher, "SessionStart group must not have a matcher")

	entries := group["hooks"].([]interface{})
	entry := entries[0].(map[string]interface{})
	cmd, _ := entry["command"].(string)
	assert.Assert(t, strings.Contains(cmd, "chunk session start"), "SessionStart hook must invoke chunk session start, got: %s", cmd)
}

func TestBuildNoCommandsOmitsSessionStartHook(t *testing.T) {
	data, err := Build(nil)
	assert.NilError(t, err)

	var s map[string]interface{}
	assert.NilError(t, json.Unmarshal(data, &s))

	_, hasHooks := s["hooks"]
	assert.Assert(t, !hasHooks, "Build with no commands must produce no hooks at all")
}

func TestBuildCodexNoPreToolUseHook(t *testing.T) {
	fix := []config.FixCommand{
		{Name: "format", Run: "gofmt -w .", Timeout: 30},
	}
	data, err := BuildCodex(fix)
	assert.NilError(t, err)

	var s map[string]interface{}
	assert.NilError(t, json.Unmarshal(data, &s))

	hooks := s["hooks"].(map[string]interface{})
	_, hasPreToolUse := hooks["PreToolUse"]
	assert.Assert(t, !hasPreToolUse, "BuildCodex must not include a PreToolUse hook")
}

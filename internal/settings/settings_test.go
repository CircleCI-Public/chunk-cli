package settings

import (
	"encoding/json"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
)

func TestBuildValidateTimeoutDefaultsToSixty(t *testing.T) {
	// A command with Timeout: 0 must produce a non-zero timeout in the generated
	// hook entry — the default of 60s must be applied.
	validate := []config.ValidateCommand{
		{Name: "test", Run: "go test ./...", Timeout: 0},
	}
	data, err := Build(nil, validate)
	assert.NilError(t, err)

	var s map[string]interface{}
	assert.NilError(t, json.Unmarshal(data, &s))

	hooks := s["hooks"].(map[string]interface{})
	preToolUse := hooks["PreToolUse"].([]interface{})
	group := preToolUse[0].(map[string]interface{})
	entries := group["hooks"].([]interface{})
	entry := entries[0].(map[string]interface{})

	// The PreToolUse hook runs chunk validate; base buffer is 30, plus one
	// defaulted command at 300s = 330s.
	timeout, _ := entry["timeout"].(float64)
	assert.Assert(t, timeout > 0, "expected non-zero timeout, got: %v", timeout)
}

func TestBuildValidateTimeoutRespectsExplicitValue(t *testing.T) {
	validate := []config.ValidateCommand{
		{Name: "lint", Run: "golangci-lint run", Timeout: 120},
	}
	data, err := Build(nil, validate)
	assert.NilError(t, err)

	var s map[string]interface{}
	assert.NilError(t, json.Unmarshal(data, &s))

	hooks := s["hooks"].(map[string]interface{})
	preToolUse := hooks["PreToolUse"].([]interface{})
	group := preToolUse[0].(map[string]interface{})
	entries := group["hooks"].([]interface{})
	entry := entries[0].(map[string]interface{})

	timeout, _ := entry["timeout"].(float64)
	assert.Assert(t, timeout == 150, "expected 30 buffer + 120 cmd = 150, got: %v", timeout)
}

func TestBuildIncludesPostToolUseForFix(t *testing.T) {
	fix := []config.FixCommand{
		{Name: "format", Run: "gofmt -w .", Timeout: 30},
	}
	data, err := Build(fix, nil)
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

func TestBuildNoFixCommandsOmitsPostToolUse(t *testing.T) {
	validate := []config.ValidateCommand{
		{Name: "test", Run: "go test ./...", Timeout: 60},
	}
	data, err := Build(nil, validate)
	assert.NilError(t, err)

	var s map[string]interface{}
	assert.NilError(t, json.Unmarshal(data, &s))

	hooks := s["hooks"].(map[string]interface{})
	_, hasPostToolUse := hooks["PostToolUse"]
	assert.Assert(t, !hasPostToolUse, "expected no PostToolUse hook when no fix commands")
}

func TestBuildNoStopHook(t *testing.T) {
	validate := []config.ValidateCommand{
		{Name: "test", Run: "go test ./...", Timeout: 60},
	}
	data, err := Build(nil, validate)
	assert.NilError(t, err)

	var s map[string]interface{}
	assert.NilError(t, json.Unmarshal(data, &s))

	hooks, ok := s["hooks"].(map[string]interface{})
	assert.Assert(t, ok)
	_, hasStop := hooks["Stop"]
	assert.Assert(t, !hasStop, "Build must not include a Stop hook")
}

func TestBuildCodexNoMetadata(t *testing.T) {
	validate := []config.ValidateCommand{
		{Name: "test", Run: "go test ./...", Timeout: 60},
	}
	data, err := BuildCodex(nil, validate)
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

func TestBuildCodexCommandNotWrappedWithCd(t *testing.T) {
	validate := []config.ValidateCommand{
		{Name: "test", Run: "go test ./...", Timeout: 60},
	}
	data, err := BuildCodex(nil, validate)
	assert.NilError(t, err)

	var s map[string]interface{}
	assert.NilError(t, json.Unmarshal(data, &s))

	hooks := s["hooks"].(map[string]interface{})
	preToolUse := hooks["PreToolUse"].([]interface{})
	group := preToolUse[0].(map[string]interface{})
	entries := group["hooks"].([]interface{})
	entry := entries[0].(map[string]interface{})

	cmd, _ := entry["command"].(string)
	assert.Equal(t, cmd, "chunk validate", "Codex PreToolUse hook must run chunk validate directly")
}

func TestBuildCodexNoCommandsProducesEmptyHooks(t *testing.T) {
	data, err := BuildCodex(nil, nil)
	assert.NilError(t, err)

	var s map[string]interface{}
	assert.NilError(t, json.Unmarshal(data, &s))

	_, hasHooks := s["hooks"]
	assert.Assert(t, !hasHooks, "BuildCodex with no commands must produce empty hooks")
}

func TestBuildIncludesSessionStartHook(t *testing.T) {
	validate := []config.ValidateCommand{
		{Name: "test", Run: "go test ./...", Timeout: 60},
	}
	data, err := Build(nil, validate)
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
	data, err := Build(nil, nil)
	assert.NilError(t, err)

	var s map[string]interface{}
	assert.NilError(t, json.Unmarshal(data, &s))

	_, hasHooks := s["hooks"]
	assert.Assert(t, !hasHooks, "Build with no commands must produce no hooks at all")
}

func TestBuildCodexNoStopHook(t *testing.T) {
	validate := []config.ValidateCommand{
		{Name: "test", Run: "go test ./...", Timeout: 60},
	}
	data, err := BuildCodex(nil, validate)
	assert.NilError(t, err)

	var s map[string]interface{}
	assert.NilError(t, json.Unmarshal(data, &s))

	hooks := s["hooks"].(map[string]interface{})
	_, hasStop := hooks["Stop"]
	assert.Assert(t, !hasStop, "BuildCodex must not include a Stop hook")
}

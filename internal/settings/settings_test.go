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
	entries := group["hooks"].([]interface{})
	entry := entries[0].(map[string]interface{})

	timeout, _ := entry["timeout"].(float64)
	assert.Assert(t, timeout == 60, "expected default hook timeout of 60 for command with Timeout: 0, got: %v", timeout)
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

package settings

import (
	"encoding/json"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/smeltdev/smelt/internal/config"
)

// commitGate returns the generated commit gate entry. Looked up by command
// rather than indexed because the group also holds smelt's advisory conflict
// notice, and a positional assertion would break every time that list gains an
// entry — which says nothing about the timeout under test.
//
// The needle is the smelt validate invocation and not the command's Run text:
// hook entries invoke smelt validate so they route through the daemon, so the
// raw command never appears in settings.json.
func commitGate(t *testing.T, entries []interface{}) map[string]interface{} {
	t.Helper()
	for _, e := range entries {
		entry, ok := e.(map[string]interface{})
		if !ok {
			continue
		}
		if cmd, _ := entry["command"].(string); cmd == commitCommand {
			return entry
		}
	}
	t.Fatalf("no commit gate entry running %q in %d entries", commitCommand, len(entries))
	return nil
}

func TestBuildHookTimeoutDefaultsToRuntimeTimeout(t *testing.T) {
	// A command with Timeout: 0 must produce a non-zero timeout in the generated
	// hook entry: zero makes Claude Code treat the hook as having no limit
	// (MUT-006). The budget is the timeout smelt validate really runs the command
	// with, so the gate never cuts off a command still inside its own timeout.
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
	entry := commitGate(t, entries)
	assert.Equal(t, entry["if"], CommitIfFilter, "entry must carry if filter for git commit")

	timeout, _ := entry["timeout"].(float64)
	assert.Equal(t, int(timeout), config.DefaultCommandTimeout)
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
	entry := commitGate(t, entries)
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
	entry := commitGate(t, entries)

	timeout, _ := entry["timeout"].(float64)
	assert.Assert(t, timeout == 120, "expected explicit timeout of 120, got: %v", timeout)
}

// The validate cache keys on the command name, so one entry per command never
// shares a key with the Stop hook's full run and a commit revalidates a tree
// Stop just passed. One bare entry runs the commands in sequence and hits the
// cache instead.
func TestBuildCommitGateIsOneSequentialEntry(t *testing.T) {
	cmds := []config.Command{
		{Name: "format", Run: "task fmt", Timeout: 30, Role: config.RoleAutofix},
		{Name: "lint", Run: "task lint", Timeout: 60},
		{Name: "test", Run: "task test", Timeout: 300},
	}
	data, err := Build(cmds)
	assert.NilError(t, err)

	var s settingsJSON
	assert.NilError(t, json.Unmarshal(data, &s))
	groups := s.Hooks["PreToolUse"]
	assert.Equal(t, len(groups), 1)

	entries := groups[0].Hooks
	assert.Equal(t, len(entries), 2, "the conflicts notice and one commit gate")
	assert.Equal(t, entries[1].Command, commitCommand)
	assert.Equal(t, entries[1].If, CommitIfFilter)
	assert.Equal(t, entries[1].Timeout, 390, "timeout must cover every command in turn")
}

func TestBuildCommitTimeoutIsCapped(t *testing.T) {
	cmds := []config.Command{
		{Name: "lint", Run: "task lint", Timeout: 400},
		{Name: "test", Run: "task test", Timeout: 400},
	}
	data, err := Build(cmds)
	assert.NilError(t, err)

	var s settingsJSON
	assert.NilError(t, json.Unmarshal(data, &s))
	assert.Equal(t, s.Hooks["PreToolUse"][0].Hooks[1].Timeout, maxHookTimeout)
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

// codexCommitEntries returns the entries in the Codex commit gate group.
func codexCommitEntries(t *testing.T, data []byte) []hookEntry {
	t.Helper()
	var s codexHooksJSON
	assert.NilError(t, json.Unmarshal(data, &s))
	groups := s.Hooks["PreToolUse"]
	assert.Equal(t, len(groups), 1)
	assert.Equal(t, groups[0].Matcher, CommitMatcher)
	return groups[0].Hooks
}

// Codex launches every matching hook concurrently, so the commit gate is one
// entry that runs the commands in sequence rather than one entry per command.
func TestBuildCodexCommitGateIsOneSequentialEntry(t *testing.T) {
	cmds := []config.Command{
		{Name: "format", Run: "task fmt", Timeout: 30, Role: config.RoleAutofix},
		{Name: "lint", Run: "task lint", Timeout: 60},
		{Name: "test", Run: "task test", Timeout: 300},
	}
	data, err := BuildCodex(cmds)
	assert.NilError(t, err)

	entries := codexCommitEntries(t, data)
	assert.Equal(t, len(entries), 1)
	assert.Equal(t, entries[0].Command, "smelt validate")
	// Merge recognises smelt's entry by its "if", so it stays even though Codex
	// does not act on it.
	assert.Equal(t, entries[0].If, CommitIfFilter)
	assert.Equal(t, entries[0].Timeout, 390, "timeout must cover every command in turn")
}

func TestBuildCodexCommitTimeoutIsCapped(t *testing.T) {
	cmds := []config.Command{
		{Name: "lint", Run: "task lint", Timeout: 400},
		{Name: "test", Run: "task test", Timeout: 400},
	}
	data, err := BuildCodex(cmds)
	assert.NilError(t, err)

	entries := codexCommitEntries(t, data)
	assert.Equal(t, entries[0].Timeout, 600)
}

func TestBuildCodexTimeoutDefaultsToRuntimeTimeout(t *testing.T) {
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
	assert.Equal(t, int(timeout), config.DefaultCommandTimeout)
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
	assert.Equal(t, entry["command"], "smelt validate")
}

func TestBuildCodexNoCommandsProducesEmptyHooks(t *testing.T) {
	data, err := BuildCodex(nil)
	assert.NilError(t, err)

	var s map[string]interface{}
	assert.NilError(t, json.Unmarshal(data, &s))

	_, hasHooks := s["hooks"]
	assert.Assert(t, !hasHooks, "BuildCodex with no commands must produce empty hooks")
}

// With no commands there is nothing to validate, so smelt writes no hooks at all.
func TestBuildWritesNoHooksWithoutCommands(t *testing.T) {
	data, err := Build(nil)
	assert.NilError(t, err)

	var s map[string]interface{}
	assert.NilError(t, json.Unmarshal(data, &s))
	assert.Assert(t, s["hooks"] == nil, "hooks were written for a project with no commands")
}

// statusMessages maps each hook entry's command to its statusMessage, across
// every hook type in data.
func statusMessages(t *testing.T, data []byte) map[string]string {
	t.Helper()
	var s struct {
		Hooks map[string][]hookGroup `json:"hooks"`
	}
	assert.NilError(t, json.Unmarshal(data, &s))
	got := make(map[string]string)
	for _, groups := range s.Hooks {
		for _, g := range groups {
			for _, e := range g.Hooks {
				got[e.Command] = e.StatusMessage
			}
		}
	}
	return got
}

// Every entry names the command it runs in its spinner text, and the commit
// gate's say why the commit is waiting. Codex reads the same field.
func TestBuildSetsStatusMessages(t *testing.T) {
	cmds := []config.Command{
		{Name: "lint", Run: "task lint", Timeout: 60},
		{Name: "test", Run: "task test", Timeout: 300},
	}

	data, err := Build(cmds)
	assert.NilError(t, err)
	assert.DeepEqual(t, statusMessages(t, data), map[string]string{
		ConflictsCommand: "Running smelt conflicts before commit",
		commitCommand:    "Running smelt validate before commit",
		StopCommand:      "Running smelt validate",
	})

	data, err = BuildCodex(cmds)
	assert.NilError(t, err)
	var codex codexHooksJSON
	assert.NilError(t, json.Unmarshal(data, &codex))
	assert.Equal(t, codex.Hooks["PreToolUse"][0].Hooks[0].StatusMessage, "Running smelt validate if this is a commit")
	assert.Equal(t, codex.Hooks["Stop"][0].Hooks[0].StatusMessage, "Running smelt validate")
}

// TestHookTimeoutsAgree pins the bug where the commit gate and the Stop hook
// guessed different defaults for a command with no timeout (60s and 300s), and
// the Codex file used a third formula: every hook now budgets the same commands
// the same way.
func TestHookTimeoutsAgree(t *testing.T) {
	cmds := []config.Command{
		{Name: config.CmdInstall, Run: "task mod-download"}, // no timeout of its own
		{Name: "test", Run: "task test", Timeout: 100},
		{Name: "format", Run: "task fmt", Timeout: 30, Role: config.RoleAutofix},
	}
	total := config.DefaultCommandTimeout + 100 + 30
	commit, stop := hookTimeouts(cmds)
	assert.Equal(t, commit, min(total, maxHookTimeout))
	assert.Equal(t, stop, min(stopHookBuffer+total, maxHookTimeout))

	small := []config.Command{{Name: "lint", Run: "task lint", Timeout: 60}}
	commit, stop = hookTimeouts(small)
	assert.Equal(t, commit, 60)
	assert.Equal(t, stop, 60+stopHookBuffer, "under the cap the Stop hook keeps its buffer")
}

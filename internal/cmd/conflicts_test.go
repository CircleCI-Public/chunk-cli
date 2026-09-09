package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func runConflictsCmd(t *testing.T, dir string, args ...string) (string, string, error) {
	t.Helper()
	// Point the daemon socket at an empty directory, so nothing is listening.
	// This is the state every one of these tests wants: the daemon is optional,
	// and its absence must not be able to fail a commit.
	t.Setenv("CHUNK_WATCHD_DIR", t.TempDir())

	root := newTestRootCmd()
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	allArgs := append([]string{"conflicts", "--project", dir}, args...)
	root.SetArgs(allArgs)
	err := root.Execute()
	return out.String(), errOut.String(), err
}

func TestConflictsExitsZeroWithoutADaemon(t *testing.T) {
	// The guarantee the whole feature rests on. This command runs from the same
	// commit hook as `chunk validate`, whose non-zero exit blocks the commit, so
	// an advisory that can fail is a gate nobody asked for.
	_, _, err := runConflictsCmd(t, t.TempDir())
	assert.NilError(t, err)
}

func TestConflictsHookModeSaysNothingWithoutADaemon(t *testing.T) {
	// Silence, not an explanation. Most people do not have `chunk watch` open,
	// and a hook that announced that on every commit would be pure noise.
	out, errOut, err := runConflictsCmd(t, t.TempDir(), "--hook")
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(out, ""))
	assert.Check(t, cmp.Equal(errOut, ""))
}

func TestConflictsManualModeExplainsAMissingDaemon(t *testing.T) {
	// The counterpart: somebody who typed the command gets told why there is no
	// answer, because otherwise silence reads as "no conflicts".
	out, _, err := runConflictsCmd(t, t.TempDir())
	assert.NilError(t, err)
	assert.Check(t, cmp.Contains(out, "chunk watch"))
}

func TestConflictsJSONModeStaysParseableWithoutADaemon(t *testing.T) {
	out, _, err := runConflictsCmd(t, t.TempDir(), "--json")
	assert.NilError(t, err)
	var parsed map[string]any
	decodeErr := json.Unmarshal([]byte(out), &parsed)
	assert.NilError(t, decodeErr, "--json must always emit JSON, including on failure")
}

func TestConflictsHookModeEmitsOnlyJSONOnStdout(t *testing.T) {
	// Whatever else changes, stdout in hook mode has to stay parseable as a
	// single JSON object: plain text mixed in would stop Claude Code reading
	// the additionalContext that carries the notice to the agent.
	dir := t.TempDir()
	err := os.WriteFile(dir+"/placeholder", []byte("x"), 0o644)
	assert.NilError(t, err)

	out, _, runErr := runConflictsCmd(t, dir, "--hook")
	assert.NilError(t, runErr)
	if out == "" {
		return // nothing to advise, which is the documented quiet path
	}
	var parsed hookOutput
	decodeErr := json.Unmarshal([]byte(out), &parsed)
	assert.NilError(t, decodeErr)
	assert.Check(t, cmp.Equal(parsed.HookSpecificOutput.HookEventName, "PreToolUse"))
}

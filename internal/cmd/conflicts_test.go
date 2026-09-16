package cmd

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/watchd"
)

// noDaemon points the daemon socket at an empty directory, so nothing is
// listening. The daemon is optional, and its absence must not be able to fail a
// commit — so most of these tests want exactly this state.
//
// Set per-test rather than inside runConflictsCmd: a helper that silently made
// the daemon unreachable is what let the hook-mode stdout test pass without ever
// reaching its assertions.
func noDaemon(t *testing.T) {
	t.Helper()
	t.Setenv("CHUNK_WATCHD_DIR", t.TempDir())
}

// serveConflicts stands up a Unix socket answering /conflicts the way the daemon
// does, so the command has an answer to report. A fake daemon rather than a
// stubbed fetch: what stdout carries is only worth asserting on if it came out
// of the same socket read the hook really performs.
func serveConflicts(t *testing.T, report watchd.ConflictReport) {
	t.Helper()
	// Not t.TempDir(): it embeds the test name, and a unix socket path is capped
	// at 104 bytes on darwin, so a descriptive name silently breaks listen.
	dir, err := os.MkdirTemp("", "wd")
	assert.NilError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("CHUNK_WATCHD_DIR", dir)

	mux := http.NewServeMux()
	mux.HandleFunc("/conflicts", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(report)
	})
	ln, err := net.Listen("unix", filepath.Join(dir, "watchd.sock"))
	assert.NilError(t, err)
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
}

// conflictedReport is a daemon answer that does have something to advise, which
// is the only state in which hook mode prints anything at all.
func conflictedReport() watchd.ConflictReport {
	return watchd.ConflictReport{
		Root:  "/repo",
		Known: true,
		Conflict: &watchd.ConflictState{
			Branch:          "feature",
			Target:          "origin/main",
			Conflicted:      true,
			Paths:           []string{"internal/cmd/conflicts.go"},
			TotalPaths:      1,
			CheckedAt:       time.Now(),
			TargetFetchedAt: time.Now(),
		},
	}
}

func runConflictsCmd(t *testing.T, dir string, args ...string) (string, string, error) {
	t.Helper()
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
	noDaemon(t)
	_, _, err := runConflictsCmd(t, t.TempDir())
	assert.NilError(t, err)
}

func TestConflictsHookModeSaysNothingWithoutADaemon(t *testing.T) {
	// Silence, not an explanation. Most people do not have `chunk watch` open,
	// and a hook that announced that on every commit would be pure noise.
	noDaemon(t)
	out, errOut, err := runConflictsCmd(t, t.TempDir(), "--hook")
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(out, ""))
	assert.Check(t, cmp.Equal(errOut, ""))
}

func TestConflictsManualModeExplainsAMissingDaemon(t *testing.T) {
	// The counterpart: somebody who typed the command gets told why there is no
	// answer, because otherwise silence reads as "no conflicts".
	noDaemon(t)
	out, _, err := runConflictsCmd(t, t.TempDir())
	assert.NilError(t, err)
	assert.Check(t, cmp.Contains(out, "chunk watch"))
}

func TestConflictsJSONModeStaysParseableWithoutADaemon(t *testing.T) {
	noDaemon(t)
	out, _, err := runConflictsCmd(t, t.TempDir(), "--json")
	assert.NilError(t, err)
	var parsed map[string]any
	decodeErr := json.Unmarshal([]byte(out), &parsed)
	assert.NilError(t, decodeErr, "--json must always emit JSON, including on failure")
}

func TestConflictsJSONModeKeepsOneShapeWithoutADaemon(t *testing.T) {
	// Parseable is not enough: it has to parse as a ConflictReport. A separate
	// {"error": ...} object would make .known and .conflict absent exactly when
	// a script is least likely to have tested for it — nobody had a daemon
	// running when they wrote the script.
	noDaemon(t)
	out, _, err := runConflictsCmd(t, t.TempDir(), "--json")
	assert.NilError(t, err)

	var report watchd.ConflictReport
	assert.NilError(t, json.Unmarshal([]byte(out), &report))
	assert.Check(t, !report.Known, "an unreachable daemon is not a known project")
	assert.Assert(t, report.Conflict != nil, "the reason must survive as a report, not an error object")
	assert.Check(t, cmp.Contains(report.Conflict.Unavailable, "daemon"))
	// Nothing is claimed about the merge itself.
	assert.Check(t, !report.Conflict.Conflicted)

	// The keys a consumer reads are present, and the old shape is gone rather
	// than emitted alongside the new one.
	var raw map[string]any
	assert.NilError(t, json.Unmarshal([]byte(out), &raw))
	_, hasKnown := raw["known"]
	assert.Check(t, hasKnown, "known must always be present")
	_, hasError := raw["error"]
	assert.Check(t, !hasError, `--json must not fall back to an {"error": ...} object`)
}

func TestConflictsJSONModeReportsTheDaemonsAnswer(t *testing.T) {
	// The other half of "one shape": the success path has to keep producing the
	// report the no-daemon path now imitates.
	serveConflicts(t, conflictedReport())

	out, _, err := runConflictsCmd(t, t.TempDir(), "--json")
	assert.NilError(t, err)

	var report watchd.ConflictReport
	assert.NilError(t, json.Unmarshal([]byte(out), &report))
	assert.Check(t, report.Known)
	assert.Assert(t, report.Conflict != nil)
	assert.Check(t, report.Conflict.Conflicted)
	assert.Check(t, cmp.Equal(report.Conflict.Target, "origin/main"))
}

func TestConflictsHookModeEmitsOnlyJSONOnStdout(t *testing.T) {
	// Whatever else changes, stdout in hook mode has to stay parseable as a
	// single JSON object: plain text mixed in would stop Claude Code reading
	// the additionalContext that carries the notice to the agent.
	//
	// The daemon has to be answering with a conflict for this to test anything.
	// Without one the command is silent by design, and an empty stdout parses as
	// nothing at all.
	serveConflicts(t, conflictedReport())

	out, errOut, err := runConflictsCmd(t, t.TempDir(), "--hook")
	assert.NilError(t, err)
	assert.Check(t, out != "", "hook mode must report a conflict the daemon knows about")

	// Unmarshal of the whole buffer is the assertion: it fails on anything
	// trailing the object, which is how stray text would show up.
	var parsed hookOutput
	assert.NilError(t, json.Unmarshal([]byte(out), &parsed))
	assert.Check(t, cmp.Equal(parsed.HookSpecificOutput.HookEventName, "PreToolUse"))
	assert.Check(t, cmp.Contains(parsed.HookSpecificOutput.AdditionalContext,
		"does not currently merge cleanly"))

	// The human-readable copy belongs on stderr. Finding it there is what proves
	// stdout stayed JSON because the text went elsewhere, not because the command
	// had nothing to say.
	assert.Check(t, cmp.Contains(errOut, "Merge conflict advisory"))
}

func TestConflictsHookModeSaysNothingWhenTheMergeIsClean(t *testing.T) {
	// The quiet path, with a daemon actually answering. The old version of the
	// stdout test above leaned on this case without ever reaching it, and a
	// daemon that is running and reports a clean merge is the state most commits
	// are in.
	report := conflictedReport()
	report.Conflict.Conflicted = false
	report.Conflict.Paths = nil
	report.Conflict.TotalPaths = 0
	serveConflicts(t, report)

	out, errOut, err := runConflictsCmd(t, t.TempDir(), "--hook")
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(out, ""))
	assert.Check(t, cmp.Equal(errOut, ""))
}

package factory

import (
	"context"
	"errors"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/smeltdev/smelt/internal/iostream"
	"github.com/smeltdev/smelt/internal/review"
	"github.com/smeltdev/smelt/internal/sidecar"
)

const notesJSON = `{"is_error":false,"structured_output":{"title":"Add a flag","what":"- a flag","why":"- users asked","how":"- cmd/flag.go","extra":1}}`

func TestParseNotes(t *testing.T) {
	n, err := ParseNotes(notesJSON)
	assert.NilError(t, err)
	assert.DeepEqual(t, n, Notes{Title: "Add a flag", What: "- a flag", Why: "- users asked", How: "- cmd/flag.go"})

	for name, out := range map[string]string{
		"missing field": `{"structured_output":{"title":"t","what":"w","why":"y"}}`,
		"not json":      `here are your notes`,
		"no output":     `{"is_error":true,"subtype":"error_max_turns","result":"gave up"}`,
	} {
		_, err := ParseNotes(out)
		assert.Assert(t, err != nil, name)
	}

	long := strings.Repeat("word ", 30)
	n, err = ParseNotes(`{"structured_output":{"title":"` + long + `","what":"w","why":"y","how":"h"}}`)
	assert.NilError(t, err)
	assert.Assert(t, len([]rune(n.Title)) <= maxTitleRunes, n.Title)
	assert.Assert(t, strings.HasSuffix(n.Title, "word…"), n.Title)
}

func TestRequestTitleStripsHeadingMarkers(t *testing.T) {
	for in, want := range map[string]string{
		"# Rename widget → smelt\n\nbody": "Rename widget → smelt",
		"## Title":                        "Title",
		"#Title":                          "Title",
		"  plain first line\nsecond":      "plain first line",
	} {
		assert.Equal(t, PRTitle(in), want)
	}
}

var testOutcome = Outcome{Result: ResultPassed, Rounds: 3}

func TestNotesRendering(t *testing.T) {
	n := Notes{Title: "Add a flag", What: "- a flag", Why: "- users asked", How: "- `cmd/flag.go`"}
	assert.Equal(t, n.PRBody("run-1", testOutcome, "2 files changed, 3 insertions(+)"),
		"### What\n- a flag\n\n### Why\n- users asked\n\n### How\n- `cmd/flag.go`\n\n---\nWritten by smelt factory run run-1: passed after 3 round(s).\n2 files changed, 3 insertions(+)\n")
	assert.Equal(t, n.CommitMessage("run-1", testOutcome, "2 files changed, 3 insertions(+)"),
		"Add a flag\n\nWhat:\n- a flag\n\nWhy:\n- users asked\n\nHow:\n- `cmd/flag.go`\n\nWritten by smelt factory run run-1: passed after 3 round(s).\n2 files changed, 3 insertions(+)\n")

	fb := fallbackNotes("# Big spec\n\nsecond line\nthird", "timed out")
	assert.Equal(t, fb.PRBody("run-1", testOutcome, "3 files changed"),
		"(smelt could not write a summary for this change: timed out)\n\n---\nWritten by smelt factory run run-1: passed after 3 round(s).\n3 files changed\n")
	msg := fb.CommitMessage("run-1", Outcome{}, "")
	assert.Equal(t, msg, "Big spec\n\n(smelt could not write a summary for this change: timed out)\n\nWritten by smelt factory run run-1.\n")
	assert.Assert(t, !strings.Contains(msg+fb.PRBody("run-1", testOutcome, ""), "second line"))
}

func notesSidecars(run func(script string, env map[string]string) (int, string, error)) (*Sidecars, *string) {
	var script string
	entry := &sidecar.PoolEntry{ID: "impl", RepoPath: "/repo"}
	exec := func(_ context.Context, e *sidecar.PoolEntry, s string, env map[string]string, out iostream.OutputFn, _ func(string)) (int, error) {
		script = s
		code, stdout, err := run(s, env)
		out(iostream.StreamStdout, []byte(stdout))
		return code, err
	}
	return &Sidecars{Exec: exec, Implementer: &Implementer{Entry: entry, Credential: review.Credential{EnvVar: "ANTHROPIC_API_KEY", Value: "k"}}}, &script
}

func TestWriteNotes(t *testing.T) {
	var gotEnv map[string]string
	s, script := notesSidecars(func(_ string, env map[string]string) (int, string, error) {
		gotEnv = env
		return 0, notesJSON, nil
	})
	var logged []string
	n, err := s.WriteNotes(context.Background(), NotesInput{
		Request: "# Spec\nline two", Outcome: testOutcome, DiffStat: " a.go | 2 +-", Baseline: "abc", Summary: "did it",
		Log: func(h, _ string) { logged = append(logged, h) },
	})
	assert.NilError(t, err)
	assert.Equal(t, n.Title, "Add a flag")
	assert.Equal(t, gotEnv["ANTHROPIC_API_KEY"], "k")
	assert.Assert(t, strings.Contains(*script, "--json-schema"))
	assert.Assert(t, strings.Contains(*script, "Read,Grep,Glob,Bash(git diff:*),Bash(git log:*)"))
	assert.Assert(t, !strings.Contains(*script, "Edit"))
	assert.DeepEqual(t, logged, []string{"notes prompt", "notes response"})
}

func TestWriteNotesFallsBack(t *testing.T) {
	for name, run := range map[string]func(string, map[string]string) (int, string, error){
		"exit":     func(string, map[string]string) (int, string, error) { return 1, "", nil },
		"exec":     func(string, map[string]string) (int, string, error) { return 0, "", errors.New("boom") },
		"bad json": func(string, map[string]string) (int, string, error) { return 0, "nope", nil },
	} {
		t.Run(name, func(t *testing.T) {
			s, _ := notesSidecars(run)
			n, err := s.WriteNotes(context.Background(), NotesInput{Request: "# Title\nsecret second line"})
			assert.Assert(t, err != nil)
			assert.Equal(t, n.Title, "Title")
			assert.Assert(t, n.Fallback != "")
			assert.Assert(t, !strings.Contains(n.PRBody("r", testOutcome, ""), "secret"))
		})
	}
}

func TestWriteNotesTimeout(t *testing.T) {
	s, _ := notesSidecars(func(string, map[string]string) (int, string, error) { return 0, "", nil })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.Exec = func(ctx context.Context, _ *sidecar.PoolEntry, _ string, _ map[string]string, _ iostream.OutputFn, _ func(string)) (int, error) {
		return 0, ctx.Err()
	}
	_, err := s.WriteNotes(ctx, NotesInput{Request: "x"})
	assert.Assert(t, err != nil)
}

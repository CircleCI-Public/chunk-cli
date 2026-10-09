package factory

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/claudecode"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
)

// bootstrapJSON is claude's JSON result carrying answer as its structured
// output.
func bootstrapJSON(t *testing.T, answer any) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"type": "result", "subtype": "success", "is_error": false,
		"result": "done", "total_cost_usd": 0.75, "structured_output": answer,
	})
	assert.NilError(t, err)
	return string(raw)
}

func reviewEntry(name, body string) map[string]any { return map[string]any{"name": name, "body": body} }

var bootstrapPromptRe = regexp.MustCompile(`echo (\S+) \| base64 -d`)

func TestRunBootstrapAsksClaudeToReadAndAnswerToTheSchema(t *testing.T) {
	var script string
	exec := func(_ context.Context, _ *sidecar.PoolEntry, s string, _ map[string]string, out circleci.OutputFn, _ func(string)) (int, error) {
		script = s
		out(circleci.StreamStdout, []byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"AGENTS.md"}}]}}`+"\n"))
		out(circleci.StreamStdout, []byte(bootstrapJSON(t, map[string]any{
			"summary": "A Go CLI.",
			"reviews": []any{reviewEntry("testing", "# Testing\n\nCheck the tests.")},
		})+"\n"))
		return 0, nil
	}
	var acts []claudecode.Activity

	got, err := RunBootstrap(context.Background(), BootstrapOptions{
		Exec:  exec,
		Entry: &sidecar.PoolEntry{ID: "sb-1", RepoPath: "/home/user/repo"},
		Commands: []config.Command{
			{Name: "test", Run: "go test ./..."},
			{Name: "fmt", Run: "gofmt -w .", Role: config.RoleAutofix},
		},
		OnActivity: func(a claudecode.Activity) { acts = append(acts, a) },
	})

	assert.NilError(t, err)
	assert.DeepEqual(t, acts, []claudecode.Activity{{Tool: "Read", Detail: "AGENTS.md"}})
	assert.Equal(t, got.CostUSD, 0.75)
	assert.Assert(t, strings.Contains(script, "'stream-json'"), "a long read reports what it does: %s", script)
	assert.DeepEqual(t, got.Prompts, []GeneratedPrompt{{Name: "testing", Body: "# Testing\n\nCheck the tests."}})
	assert.Equal(t, got.Summary, "A Go CLI.")
	assert.Assert(t, strings.Contains(script, "'--json-schema' "+sidecar.ShellEscape(bootstrapSchema)), script)
	assert.Assert(t, strings.Contains(script, "'--allowedTools' 'Read,Grep,Glob,"), script)
	assert.Assert(t, !strings.Contains(script, "Edit") && !strings.Contains(script, "Write"), "bootstrap must not edit the tree: %s", script)

	m := bootstrapPromptRe.FindStringSubmatch(script)
	assert.Assert(t, m != nil, script)
	prompt, err := base64.StdEncoding.DecodeString(strings.Trim(m[1], "'"))
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(string(prompt), "test: `go test ./...`"), "validation commands are named: %s", prompt)
	assert.Assert(t, !strings.Contains(string(prompt), "gofmt"), "autofix commands are not checks: %s", prompt)
}

func TestRunBootstrapPassesOnClaudesFailure(t *testing.T) {
	exec := func(_ context.Context, _ *sidecar.PoolEntry, _ string, _ map[string]string, _ circleci.OutputFn, _ func(string)) (int, error) {
		return claudecode.ExitMissing, nil
	}

	_, err := RunBootstrap(context.Background(), BootstrapOptions{Exec: exec, Entry: &sidecar.PoolEntry{ID: "sb-1"}})

	assert.Assert(t, errors.Is(err, claudecode.ErrMissing), "got %v", err)
}

func TestBootstrapPromptLeavesOutWhatTheProjectLacks(t *testing.T) {
	prompt := bootstrapPrompt(nil, "  ")

	assert.Assert(t, !strings.Contains(prompt, "validation commands also run"), prompt)
	assert.Assert(t, !strings.Contains(prompt, StandardsPath), prompt)
	assert.Assert(t, !strings.Contains(prompt, "%!"), "a format verb was left unfilled: %s", prompt)
}

func TestBootstrapPromptIncludesTheTeamsStandards(t *testing.T) {
	prompt := bootstrapPrompt(nil, "Prefer early returns.")

	assert.Assert(t, strings.Contains(prompt, StandardsPath), prompt)
	assert.Assert(t, strings.Contains(prompt, "<standards>\nPrefer early returns.\n</standards>"), prompt)
}

func TestParseBootstrapDropsUnusablePromptsAndCountsThem(t *testing.T) {
	reviews := []any{
		reviewEntry("testing", "ok"),
		reviewEntry("Bad Name", "spaces and capitals"),
		reviewEntry("../escape", "a path"),
		reviewEntry("testing", "a repeat"),
		reviewEntry("empty", "   "),
		reviewEntry("huge", strings.Repeat("x", maxPromptBody+1)),
		reviewEntry("security", "ok too"),
	}

	got, err := parseBootstrap(bootstrapJSON(t, map[string]any{"summary": "", "reviews": reviews}))

	assert.NilError(t, err)
	assert.Equal(t, got.Dropped, 5)
	assert.DeepEqual(t, got.Prompts, []GeneratedPrompt{{Name: "testing", Body: "ok"}, {Name: "security", Body: "ok too"}})
}

func TestParseBootstrapKeepsAtMostTheLimit(t *testing.T) {
	var reviews []any
	for _, name := range []string{"a", "b", "c", "d", "e", "f", "g"} {
		reviews = append(reviews, reviewEntry(name, "ok"))
	}

	got, err := parseBootstrap(bootstrapJSON(t, map[string]any{"summary": "", "reviews": reviews}))

	assert.NilError(t, err)
	assert.Equal(t, len(got.Prompts), maxBootstrapPrompts)
	assert.Equal(t, got.Dropped, 1)
}

func TestParseBootstrapFails(t *testing.T) {
	for name, tc := range map[string]struct{ out, want string }{
		"not json": {out: "Just prose.", want: "read claude's result"},
		"no structured output": {
			out:  `{"type":"result","subtype":"error_max_structured_output_retries","is_error":true,"result":"gave up"}`,
			want: "claude gave no review prompts (error_max_structured_output_retries): gave up",
		},
		"nothing usable": {
			out:  bootstrapJSON(t, map[string]any{"summary": "", "reviews": []any{reviewEntry("No Good", "x")}}),
			want: "no usable review prompts",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := parseBootstrap(tc.out)
			assert.ErrorContains(t, err, tc.want)
		})
	}
}

// The schema and the struct the answer decodes into are written apart; a field
// renamed in one alone would decode as empty without failing.
func TestBootstrapSchemaNamesTheFieldsTheAnswerReads(t *testing.T) {
	type node struct {
		Properties map[string]node `json:"properties"`
		Items      *node           `json:"items"`
	}
	var schema node
	assert.NilError(t, json.Unmarshal([]byte(bootstrapSchema), &schema))
	jsonNames := func(typ reflect.Type) []string {
		var names []string
		for f := range typ.Fields() {
			names = append(names, f.Tag.Get("json"))
		}
		slices.Sort(names)
		return names
	}

	answer := reflect.TypeFor[bootstrapAnswer]()
	assert.DeepEqual(t, slices.Sorted(maps.Keys(schema.Properties)), jsonNames(answer))
	item := schema.Properties["reviews"].Items
	assert.Assert(t, item != nil)
	field, _ := answer.FieldByName("Reviews")
	assert.DeepEqual(t, slices.Sorted(maps.Keys(item.Properties)), jsonNames(field.Type.Elem()))
}

func TestCheckPromptsDir(t *testing.T) {
	dir := t.TempDir()
	assert.NilError(t, CheckPromptsDir(filepath.Join(dir, "missing")), "a missing directory has no prompts")

	assert.NilError(t, os.WriteFile(filepath.Join(dir, "README"), []byte("notes"), 0o644))
	assert.NilError(t, os.Mkdir(filepath.Join(dir, "old.md"), 0o755))
	assert.NilError(t, CheckPromptsDir(dir), "only .md and .txt files are prompts")

	assert.NilError(t, os.WriteFile(filepath.Join(dir, "style.txt"), []byte("x"), 0o644))
	assert.Assert(t, errors.Is(CheckPromptsDir(dir), ErrPromptsExist))
}

func TestWritePromptsCreatesTheDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".chunk", "reviews")

	paths, err := WritePrompts(dir, []GeneratedPrompt{{Name: "testing", Body: "# Testing"}, {Name: "security", Body: "# Security"}})

	assert.NilError(t, err)
	assert.DeepEqual(t, paths, []string{filepath.Join(dir, "testing.md"), filepath.Join(dir, "security.md")})
	data, err := os.ReadFile(filepath.Join(dir, "testing.md"))
	assert.NilError(t, err)
	assert.Equal(t, string(data), "# Testing\n")
}

func TestWritePromptsNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "testing.md")
	assert.NilError(t, os.WriteFile(existing, []byte("mine"), 0o644))

	_, err := WritePrompts(dir, []GeneratedPrompt{{Name: "testing", Body: "theirs"}})

	assert.Assert(t, errors.Is(err, ErrPromptsExist), "got %v", err)
	data, err := os.ReadFile(existing)
	assert.NilError(t, err)
	assert.Equal(t, string(data), "mine")
}

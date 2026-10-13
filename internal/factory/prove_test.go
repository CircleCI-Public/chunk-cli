package factory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/smeltdev/smelt/internal/config"
	"github.com/smeltdev/smelt/internal/iostream"
	"github.com/smeltdev/smelt/internal/sidecar"
)

var testGates = []config.Command{{Name: "lint", Run: "golangci-lint run"}, {Name: "test", Run: "go test ./..."}}

func TestUnprovenGates(t *testing.T) {
	edit := Activity{Tool: "Edit", Detail: "main.go"}
	tests := []struct {
		name string
		acts []Activity
		want []string
	}{
		{"nothing ran", []Activity{edit}, []string{"lint", "test"}},
		{"gates by their command after the edit", []Activity{edit, {Tool: "Bash", Detail: "golangci-lint run ./..."}, {Tool: "Bash", Detail: "cd x && go test ./... -race"}}, nil},
		{"gates by the task target that wraps them", []Activity{edit, {Tool: "Bash", Detail: "task test"}, {Tool: "Bash", Detail: "task lint"}}, nil},
		{"a task target with a path", []Activity{edit, {Tool: "Bash", Detail: "task test:unit && task lint"}}, nil},
		{"a run before the last edit proves nothing", []Activity{{Tool: "Bash", Detail: "task test"}, edit, {Tool: "Bash", Detail: "task lint"}}, []string{"test"}},
		{"a later edit voids earlier runs", []Activity{edit, {Tool: "Bash", Detail: "task test"}, {Tool: "Bash", Detail: "task lint"}, {Tool: "Write", Detail: "b.go"}}, []string{"lint", "test"}},
		{"no edits at all", []Activity{{Tool: "Bash", Detail: "task test"}, {Tool: "Bash", Detail: "task lint"}}, nil},
		{"a similarly named target is not the gate", []Activity{edit, {Tool: "Bash", Detail: "task testdata"}, {Tool: "Bash", Detail: "echo task lint"}}, []string{"test"}},
		{"reading the command is not running it", []Activity{edit, {Tool: "Read", Detail: "go test ./..."}}, []string{"lint", "test"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.DeepEqual(t, unproven(tt.acts, testGates), tt.want)
		})
	}
}

func TestClaimsGatesPass(t *testing.T) {
	for summary, want := range map[string]bool{
		"All tests pass.":                           true,
		"go test passes and lint is clean":          true,
		"Gates: green":                              true,
		"Added the flag.\n\n## Gates\n```\nok\n```": true,
		"Added a pass-through wrapper.":             false,
		"Added the flag. Tests not run: no docker.": false,
		"": false,
	} {
		assert.Equal(t, claimsGatesPass(summary), want, summary)
	}
}

func TestGateCommandsLeavesOutTheMarkersCheck(t *testing.T) {
	got := gateCommands(append(append([]config.Command{}, testGates...), MarkersGate))
	assert.DeepEqual(t, got, testGates)
}

func TestSystemPromptListsTheGates(t *testing.T) {
	p := implementerSystemPrompt("be brief", testGates)
	for _, want := range []string{"## Gates heading", "- lint: golangci-lint run", "- test: go test ./...", "Run-specific instructions:\nbe brief"} {
		assert.Assert(t, contains(p, want), "missing %q in:\n%s", want, p)
	}
	assert.Assert(t, !contains(implementerSystemPrompt("", nil), "Gates"))
}

// claudeTurn is the stream of one claude turn that used tools in order, each
// "Tool:detail", and ended with result.
func claudeTurn(result string, tools ...string) string {
	var b strings.Builder
	b.WriteString(`{"type":"system","subtype":"init","session_id":"s"}` + "\n")
	for _, tool := range tools {
		name, detail, _ := strings.Cut(tool, ":")
		key := "command"
		if name != "Bash" {
			key = "file_path"
		}
		in, _ := json.Marshal(map[string]string{key: detail})
		fmt.Fprintf(&b, `{"type":"assistant","message":{"content":[{"type":"tool_use","name":%q,"input":%s}]}}`+"\n", name, in)
	}
	res, _ := json.Marshal(result)
	fmt.Fprintf(&b, `{"type":"result","subtype":"success","is_error":false,"result":%s,"total_cost_usd":0.5}`+"\n", res)
	return b.String()
}

// scriptedImplementer is a Sidecars whose implementer plays turns in order.
func scriptedImplementer(t *testing.T, turns ...string) (*Sidecars, *[]string, *[]string) {
	t.Helper()
	var prompts, notices []string
	exec := func(_ context.Context, _ *sidecar.PoolEntry, script string, _ map[string]string, out iostream.OutputFn, _ func(string)) (int, error) {
		prompts = append(prompts, promptOf(t, script))
		out(iostream.StreamStdout, []byte(turns[len(prompts)-1]))
		return 0, nil
	}
	return &Sidecars{
		Exec:        exec,
		Commands:    append(append([]config.Command{}, testGates...), MarkersGate),
		Implementer: &Implementer{Exec: exec, Entry: &sidecar.PoolEntry{ID: "impl", RepoPath: "/repo"}},
		Status:      func(_ iostream.Level, msg string) { notices = append(notices, msg) },
	}, &prompts, &notices
}

func TestImplementNudgesAClaimWithoutARun(t *testing.T) {
	s, prompts, notices := scriptedImplementer(t,
		claudeTurn("Done. All tests pass.", "Edit:main.go"),
		claudeTurn("Ran them.\n\n## Gates\nok", "Bash:task lint", "Bash:task test"))
	turn, err := s.Implement(context.Background(), "do it")
	assert.NilError(t, err)

	assert.Equal(t, len(*prompts), 2, "exactly one nudge")
	assert.Assert(t, contains((*prompts)[1], "weren't run after your last edit"), (*prompts)[1])
	assert.Assert(t, contains((*prompts)[1], "lint, test"), (*prompts)[1])
	assert.Assert(t, turn.Nudged)
	assert.Equal(t, len(turn.Unproven), 0)
	assert.Assert(t, contains(turn.Summary, "Ran them."))
	assert.Equal(t, turn.CostUSD, 1.0, "both turns are paid for")
	assert.Equal(t, len(*notices), 1)
}

func TestImplementDoesNotNudgeWhenTheGatesRan(t *testing.T) {
	s, prompts, notices := scriptedImplementer(t,
		claudeTurn("Done. All tests pass.", "Edit:main.go", "Bash:task test", "Bash:golangci-lint run ./..."))
	turn, err := s.Implement(context.Background(), "do it")
	assert.NilError(t, err)
	assert.Equal(t, len(*prompts), 1)
	assert.Assert(t, !turn.Nudged)
	assert.Equal(t, len(*notices), 0)
}

func TestImplementDoesNotNudgeWithoutAClaim(t *testing.T) {
	s, prompts, _ := scriptedImplementer(t, claudeTurn("Added the flag. I did not run anything.", "Edit:main.go"))
	_, err := s.Implement(context.Background(), "do it")
	assert.NilError(t, err)
	assert.Equal(t, len(*prompts), 1)
}

func TestImplementReportsASecondMissAndMovesOn(t *testing.T) {
	s, prompts, notices := scriptedImplementer(t,
		claudeTurn("All tests pass.", "Edit:main.go"),
		claudeTurn("Really, the tests pass.", "Bash:task lint"),
		claudeTurn("never asked"))
	turn, err := s.Implement(context.Background(), "do it")
	assert.NilError(t, err)
	assert.Equal(t, len(*prompts), 2, "a second miss is not nudged again")
	assert.Assert(t, turn.Nudged)
	assert.DeepEqual(t, turn.Unproven, []string{"test"})
	assert.Equal(t, len(*notices), 2)
	assert.Assert(t, contains((*notices)[1], "still claims"), (*notices)[1])
}

func TestImplementWithoutGatesIsNotChecked(t *testing.T) {
	s, prompts, _ := scriptedImplementer(t, claudeTurn("All tests pass.", "Edit:main.go"))
	s.Commands = []config.Command{MarkersGate}
	_, err := s.Implement(context.Background(), "do it")
	assert.NilError(t, err)
	assert.Equal(t, len(*prompts), 1)
}

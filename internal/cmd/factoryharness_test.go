package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/smeltdev/smelt/internal/config"
	"github.com/smeltdev/smelt/internal/iostream"
)

func TestDecideHarness(t *testing.T) {
	tests := []struct {
		name   string
		in     harnessInputs
		want   string
		action harnessAction
	}{
		{"flag beats config", harnessInputs{Flag: "codex", Config: "claude", CanPrompt: true}, "codex", harnessUse},
		{"config beats the prompt", harnessInputs{Config: "claude", CanPrompt: true}, "claude", harnessUse},
		{"a terminal is asked", harnessInputs{CanPrompt: true}, "", harnessAsk},
		{"no terminal and nothing set fails", harnessInputs{}, "", harnessFail},
		{"a flag needs no terminal", harnessInputs{Flag: "claude"}, "claude", harnessUse},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, action := decideHarness(tt.in)
			assert.Equal(t, got, tt.want)
			assert.Equal(t, action, tt.action)
		})
	}
}

func TestResolveHarnessFailsWithoutAnyChoiceOrTerminal(t *testing.T) {
	_, err := resolveHarness(t.Context(), iostream.Streams{Out: os.Stdout, Err: os.Stderr}, harnessChoice{Cfg: &config.ProjectConfig{}})
	var ue *userError
	assert.ErrorContains(t, err, "No coding agent is chosen")
	assert.Assert(t, asUserError(err, &ue))
	assert.Assert(t, strings.Contains(ue.suggestion, "factory.harness") && strings.Contains(ue.suggestion, "--harness"), ue.suggestion)
}

func TestResolveHarnessRejectsAnUnknownFlag(t *testing.T) {
	_, err := resolveHarness(t.Context(), iostream.Streams{Out: os.Stdout, Err: os.Stderr}, harnessChoice{Flag: "gemini"})
	assert.ErrorContains(t, err, `Unknown harness "gemini"`)
}

func TestFactoryBuildNeedsAHarnessWhenNothingIsSet(t *testing.T) {
	project := factoryProject(t)
	cfg := `{"orgID":"org-1","factory":{"backend":"circleci"},"commands":[{"name":"test","run":"go test ./..."}]}`
	assert.NilError(t, os.WriteFile(filepath.Join(project, ".smelt", "config.json"), []byte(cfg), 0o644))
	_, _, err := runFactoryBuildCmd(t, "add a flag")
	assert.ErrorContains(t, err, "No coding agent is chosen")
}

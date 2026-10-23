package cmd

import (
	"errors"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/factory"
)

func TestAgentStartError(t *testing.T) {
	for _, tc := range []struct {
		name           string
		err            error
		wantSuggestion string
	}{
		{name: "no claude", err: factory.ErrNoClaude, wantSuggestion: "Install it from https://claude.com/claude-code."},
		{name: "no factory", err: factory.ErrNoFactory, wantSuggestion: "Install factory and put it on PATH."},
		{name: "no factory plugin", err: factory.ErrNoFactoryPlugin, wantSuggestion: "Install factory's f plugin, or pass --plugin-dir."},
		{name: "factory mcp", err: factory.ErrFactoryMCP, wantSuggestion: "Make sure factory is on PATH."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := agentStartError(tc.err)
			var ue *userError
			assert.Assert(t, errors.As(got, &ue), "got %T: %v", got, got)
			assert.Equal(t, ue.Suggestion(), tc.wantSuggestion)
		})
	}
}

func TestAgentStartErrorWrapsOthers(t *testing.T) {
	other := errors.New("boom")
	got := agentStartError(other)
	assert.Assert(t, errors.Is(got, other), "got %v", got)
	var ue *userError
	assert.Assert(t, !errors.As(got, &ue), "did not expect a userError, got %v", got)
}

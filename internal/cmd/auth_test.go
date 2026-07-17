package cmd

import (
	"errors"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
)

func TestAuthSignupAlreadyAuthenticated(t *testing.T) {
	isolateConfig(t)
	t.Setenv(config.EnvCircleToken, "test-token-abc")

	cmd := newAuthSignupCmd()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true

	err := cmd.Execute()
	assert.Assert(t, err != nil)

	var ue *userError
	assert.Assert(t, errors.As(err, &ue))
	assert.Assert(t, strings.Contains(ue.UserMessage(), "Already authenticated."),
		"expected 'Already authenticated.' in message, got: %s", ue.UserMessage())
	assert.Assert(t, strings.Contains(ue.Suggestion(), "chunk auth remove"),
		"expected suggestion to mention 'chunk auth remove', got: %s", ue.Suggestion())
}

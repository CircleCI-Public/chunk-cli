package cmd

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/secrets"
)

func TestSecretResolveErrorOpNotInstalled(t *testing.T) {
	err := secretResolveError(fmt.Errorf("resolve secret for SECRET: %w",
		fmt.Errorf("%w: %w", secrets.ErrOpNotFound, exec.ErrNotFound)))

	var ue *userError
	assert.Assert(t, errors.As(err, &ue), "expected a userError, got %T", err)
	assert.Equal(t, ue.code, "secrets.op_not_found")
	assert.Equal(t, ue.UserMessage(), "Could not resolve a secret reference.")
	assert.Assert(t, strings.Contains(ue.Suggestion(), "1password.com"),
		"suggestion should point at the CLI install docs: %q", ue.Suggestion())
	// The cause must survive unwrapping so the key and reason still print.
	assert.Assert(t, strings.Contains(ue.Error(), "SECRET"), "got: %s", ue.Error())
}

func TestSecretResolveErrorOtherFailure(t *testing.T) {
	err := secretResolveError(errors.New("op read: unknown vault"))

	var ue *userError
	assert.Assert(t, errors.As(err, &ue), "expected a userError, got %T", err)
	assert.Equal(t, ue.code, "secrets.resolve_failed")
	assert.Assert(t, strings.Contains(ue.Suggestion(), "op signin"),
		"suggestion should cover sign-in: %q", ue.Suggestion())
	assert.Assert(t, !strings.Contains(ue.Suggestion(), "1password.com"),
		"install advice is wrong when op is present: %q", ue.Suggestion())
}

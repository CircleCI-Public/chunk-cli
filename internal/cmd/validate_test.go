package cmd

import (
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
)

func TestHostForwardEnv(t *testing.T) {
	t.Run("returns nil when no tokens are set", func(t *testing.T) {
		t.Setenv(config.EnvCircleToken, "")
		t.Setenv(config.EnvCircleCIToken, "")

		assert.Assert(t, hostForwardEnv() == nil)
	})

	t.Run("forwards CIRCLE_TOKEN when set", func(t *testing.T) {
		t.Setenv(config.EnvCircleToken, "abc123")
		t.Setenv(config.EnvCircleCIToken, "")

		env := hostForwardEnv()
		assert.Equal(t, env[config.EnvCircleToken], "abc123")
		_, hasAlias := env[config.EnvCircleCIToken]
		assert.Assert(t, !hasAlias)
	})

	t.Run("forwards CIRCLECI_TOKEN when set", func(t *testing.T) {
		t.Setenv(config.EnvCircleToken, "")
		t.Setenv(config.EnvCircleCIToken, "def456")

		env := hostForwardEnv()
		assert.Equal(t, env[config.EnvCircleCIToken], "def456")
		_, hasCanonical := env[config.EnvCircleToken]
		assert.Assert(t, !hasCanonical)
	})

	t.Run("forwards both when both are set", func(t *testing.T) {
		t.Setenv(config.EnvCircleToken, "abc123")
		t.Setenv(config.EnvCircleCIToken, "def456")

		env := hostForwardEnv()
		assert.Equal(t, env[config.EnvCircleToken], "abc123")
		assert.Equal(t, env[config.EnvCircleCIToken], "def456")
	})
}

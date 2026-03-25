package acceptance

import (
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/testing/binary"
	testenv "github.com/CircleCI-Public/chunk-cli/internal/testing/env"
)

func TestUpgradeNoGhCLI(t *testing.T) {
	env := testenv.NewTestEnv(t)
	// Remove PATH so gh cannot be found
	env.Extra["PATH"] = "/nonexistent"

	result := binary.RunCLI(t, []string{"upgrade"}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit when gh is missing")
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "gh") || strings.Contains(combined, "cli.github.com"),
		"expected gh CLI error message, got: %s", combined)
}

func TestUpgradeGhNotAuthenticated(t *testing.T) {
	env := testenv.NewTestEnv(t)
	// gh auth status will fail if GH_CONFIG_DIR points to an empty config
	env.Extra["GH_CONFIG_DIR"] = t.TempDir()

	result := binary.RunCLI(t, []string{"upgrade"}, env, env.HomeDir)

	// gh is available but not authenticated — expect error
	assert.Assert(t, result.ExitCode != 0,
		"expected non-zero exit when gh is not authenticated, stdout: %s, stderr: %s",
		result.Stdout, result.Stderr)
}

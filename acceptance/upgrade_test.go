package acceptance

import (
	"os"
	"path/filepath"
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

func TestUpgradeExtensionNotInstalled(t *testing.T) {
	// Create a fake gh script that passes auth but fails on extension upgrade
	fakeGhDir := t.TempDir()
	fakeGh := filepath.Join(fakeGhDir, "gh")
	script := `#!/bin/sh
if [ "$1" = "auth" ] && [ "$2" = "status" ]; then
  exit 0
fi
if [ "$1" = "extension" ] && [ "$2" = "upgrade" ]; then
  echo "no extension found" >&2
  exit 1
fi
exit 1
`
	err := os.WriteFile(fakeGh, []byte(script), 0o755)
	assert.NilError(t, err)

	env := testenv.NewTestEnv(t)
	env.Extra["PATH"] = fakeGhDir

	result := binary.RunCLI(t, []string{"upgrade"}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0,
		"expected non-zero exit when extension not installed")
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "upgrade failed"),
		"expected 'upgrade failed' error, got: %s", combined)
}

func TestUpgradeHappyPath(t *testing.T) {
	// Create a fake gh script that succeeds for both auth and extension upgrade
	fakeGhDir := t.TempDir()
	fakeGh := filepath.Join(fakeGhDir, "gh")
	script := `#!/bin/sh
if [ "$1" = "auth" ] && [ "$2" = "status" ]; then
  exit 0
fi
if [ "$1" = "extension" ] && [ "$2" = "upgrade" ]; then
  exit 0
fi
exit 1
`
	err := os.WriteFile(fakeGh, []byte(script), 0o755)
	assert.NilError(t, err)

	env := testenv.NewTestEnv(t)
	env.Extra["PATH"] = fakeGhDir

	result := binary.RunCLI(t, []string{"upgrade"}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0,
		"expected successful upgrade, stdout: %s, stderr: %s",
		result.Stdout, result.Stderr)
}

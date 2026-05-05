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

func TestUpgradeNoBrewCLI(t *testing.T) {
	env := testenv.NewTestEnv(t)
	env.Extra["PATH"] = "/nonexistent"

	result := binary.RunCLI(t, []string{"upgrade"}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit when brew is missing")
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "brew") || strings.Contains(combined, "brew.sh"),
		"expected brew not found error message, got: %s", combined)
}

func TestUpgradeBrewFails(t *testing.T) {
	fakeBrewDir := t.TempDir()
	fakeBrew := filepath.Join(fakeBrewDir, "brew")
	script := `#!/bin/sh
echo "Error: No available formula with the name" >&2
exit 1
`
	err := os.WriteFile(fakeBrew, []byte(script), 0o755)
	assert.NilError(t, err)

	env := testenv.NewTestEnv(t)
	env.Extra["PATH"] = fakeBrewDir

	result := binary.RunCLI(t, []string{"upgrade"}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit when brew upgrade fails")
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "upgrade failed"),
		"expected 'upgrade failed' error, got: %s", combined)
}

func TestUpgradeHappyPath(t *testing.T) {
	fakeBrewDir := t.TempDir()
	fakeBrew := filepath.Join(fakeBrewDir, "brew")
	script := `#!/bin/sh
exit 0
`
	err := os.WriteFile(fakeBrew, []byte(script), 0o755)
	assert.NilError(t, err)

	env := testenv.NewTestEnv(t)
	env.Extra["PATH"] = fakeBrewDir

	result := binary.RunCLI(t, []string{"upgrade"}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0,
		"expected successful upgrade, stdout: %s, stderr: %s",
		result.Stdout, result.Stderr)
}

package acceptance

import (
	"os"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/acceptance/testutil"
)

func TestCompletionInstall(t *testing.T) {
	env := testutil.NewTestEnv(t)
	env.Extra["SHELL"] = "/bin/zsh"

	zshrc := filepath.Join(env.HomeDir, ".zshrc")
	err := os.WriteFile(zshrc, []byte("# zshrc\n"), 0o644)
	assert.NilError(t, err)

	result := testutil.RunCLI(t, []string{"completion", "install"}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
}

func TestCompletionUninstall(t *testing.T) {
	env := testutil.NewTestEnv(t)
	env.Extra["SHELL"] = "/bin/zsh"

	result := testutil.RunCLI(t, []string{"completion", "uninstall"}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
}

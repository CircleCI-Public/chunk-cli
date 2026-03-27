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

func TestCompletionInstallZsh(t *testing.T) {
	env := testenv.NewTestEnv(t)
	env.Extra["SHELL"] = "/bin/zsh"

	zshrc := filepath.Join(env.HomeDir, ".zshrc")
	err := os.WriteFile(zshrc, []byte("# zshrc\n"), 0o644)
	assert.NilError(t, err)

	result := binary.RunCLI(t, []string{"completion", "install"}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
}

func TestCompletionInstallBash(t *testing.T) {
	env := testenv.NewTestEnv(t)
	env.Extra["SHELL"] = "/bin/bash"

	bashrc := filepath.Join(env.HomeDir, ".bashrc")
	err := os.WriteFile(bashrc, []byte("# bashrc\n"), 0o644)
	assert.NilError(t, err)

	result := binary.RunCLI(t, []string{"completion", "install"}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	data, err := os.ReadFile(bashrc)
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(string(data), "chunk completion bash"), "expected completion in .bashrc")
}

func TestCompletionInstallBashProfile(t *testing.T) {
	env := testenv.NewTestEnv(t)
	env.Extra["SHELL"] = "/bin/bash"

	// Simulate macOS where .bash_profile exists instead of .bashrc
	bashProfile := filepath.Join(env.HomeDir, ".bash_profile")
	err := os.WriteFile(bashProfile, []byte("# bash_profile\n"), 0o644)
	assert.NilError(t, err)

	result := binary.RunCLI(t, []string{"completion", "install"}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	data, err := os.ReadFile(bashProfile)
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(string(data), "chunk completion bash"), "expected completion in .bash_profile")
}

func TestCompletionUninstallZsh(t *testing.T) {
	env := testenv.NewTestEnv(t)
	env.Extra["SHELL"] = "/bin/zsh"

	result := binary.RunCLI(t, []string{"completion", "uninstall"}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
}

func TestCompletionUninstallBash(t *testing.T) {
	env := testenv.NewTestEnv(t)
	env.Extra["SHELL"] = "/bin/bash"

	result := binary.RunCLI(t, []string{"completion", "uninstall"}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
}

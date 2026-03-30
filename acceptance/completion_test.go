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

	data, err := os.ReadFile(zshrc)
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(string(data), "# chunk shell completion"),
		"expected completion tag in .zshrc, got: %s", string(data))
	assert.Assert(t, strings.Contains(string(data), "chunk completion zsh"),
		"expected zsh source line in .zshrc, got: %s", string(data))
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
	assert.Assert(t, strings.Contains(string(data), "# chunk shell completion"),
		"expected completion tag in .bashrc, got: %s", string(data))
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

func TestCompletionInstallIdempotent(t *testing.T) {
	env := testenv.NewTestEnv(t)
	env.Extra["SHELL"] = "/bin/zsh"

	zshrc := filepath.Join(env.HomeDir, ".zshrc")
	err := os.WriteFile(zshrc, []byte("# zshrc\n"), 0o644)
	assert.NilError(t, err)

	// First install
	result := binary.RunCLI(t, []string{"completion", "install"}, env, env.HomeDir)
	assert.Equal(t, result.ExitCode, 0, "first install failed")

	dataAfterFirst, err := os.ReadFile(zshrc)
	assert.NilError(t, err)

	// Second install should be a no-op
	result = binary.RunCLI(t, []string{"completion", "install"}, env, env.HomeDir)
	assert.Equal(t, result.ExitCode, 0, "second install failed")
	assert.Assert(t, strings.Contains(result.Stderr, "already installed"),
		"expected 'already installed' warning, got stderr: %s", result.Stderr)

	dataAfterSecond, err := os.ReadFile(zshrc)
	assert.NilError(t, err)
	assert.Equal(t, string(dataAfterFirst), string(dataAfterSecond),
		"RC file should not change on second install")
}

func TestCompletionInstallUnsupportedShell(t *testing.T) {
	env := testenv.NewTestEnv(t)
	env.Extra["SHELL"] = "/bin/fish"

	result := binary.RunCLI(t, []string{"completion", "install"}, env, env.HomeDir)
	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit for unsupported shell")

	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "Unsupported shell"),
		"expected unsupported shell error, got: %s", combined)
}

func TestCompletionInstallEmptyShell(t *testing.T) {
	env := testenv.NewTestEnv(t)
	env.Extra["SHELL"] = ""

	result := binary.RunCLI(t, []string{"completion", "install"}, env, env.HomeDir)
	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit for empty SHELL")
}

func TestCompletionInstallBashCreatesRCFile(t *testing.T) {
	env := testenv.NewTestEnv(t)
	env.Extra["SHELL"] = "/bin/bash"

	// Neither .bashrc nor .bash_profile exists, so install should create .bash_profile
	result := binary.RunCLI(t, []string{"completion", "install"}, env, env.HomeDir)
	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	// When neither exists, detectShell defaults to .bash_profile
	bashProfile := filepath.Join(env.HomeDir, ".bash_profile")
	info, err := os.Stat(bashProfile)
	assert.NilError(t, err, "expected .bash_profile to be created")

	perm := info.Mode().Perm()
	assert.Equal(t, perm, os.FileMode(0o644), "expected RC file perm 0644, got %04o", perm)

	data, err := os.ReadFile(bashProfile)
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(string(data), "chunk completion bash"),
		"expected completion line in created file, got: %s", string(data))
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

func TestCompletionInstallUninstallRoundTrip(t *testing.T) {
	env := testenv.NewTestEnv(t)
	env.Extra["SHELL"] = "/bin/zsh"

	zshrc := filepath.Join(env.HomeDir, ".zshrc")
	original := "# existing config\nexport FOO=bar\n"
	err := os.WriteFile(zshrc, []byte(original), 0o644)
	assert.NilError(t, err)

	// Install
	result := binary.RunCLI(t, []string{"completion", "install"}, env, env.HomeDir)
	assert.Equal(t, result.ExitCode, 0, "install failed")

	// Verify completion was added
	data, err := os.ReadFile(zshrc)
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(string(data), "# chunk shell completion"),
		"expected tag after install")

	// Uninstall
	result = binary.RunCLI(t, []string{"completion", "uninstall"}, env, env.HomeDir)
	assert.Equal(t, result.ExitCode, 0, "uninstall failed")

	// Verify completion block is removed
	data, err = os.ReadFile(zshrc)
	assert.NilError(t, err)
	assert.Assert(t, !strings.Contains(string(data), "# chunk shell completion"),
		"completion tag should be removed, got: %s", string(data))
	assert.Assert(t, !strings.Contains(string(data), "chunk completion zsh"),
		"source line should be removed, got: %s", string(data))
}

func TestCompletionUninstallPreservesOtherContent(t *testing.T) {
	env := testenv.NewTestEnv(t)
	env.Extra["SHELL"] = "/bin/bash"

	bashrc := filepath.Join(env.HomeDir, ".bashrc")
	original := "# my config\nexport PATH=/usr/local/bin:$PATH\nalias ll='ls -la'\n"
	err := os.WriteFile(bashrc, []byte(original), 0o644)
	assert.NilError(t, err)

	// Install then uninstall
	result := binary.RunCLI(t, []string{"completion", "install"}, env, env.HomeDir)
	assert.Equal(t, result.ExitCode, 0)
	result = binary.RunCLI(t, []string{"completion", "uninstall"}, env, env.HomeDir)
	assert.Equal(t, result.ExitCode, 0)

	data, err := os.ReadFile(bashrc)
	assert.NilError(t, err)
	content := string(data)
	assert.Assert(t, strings.Contains(content, "export PATH=/usr/local/bin:$PATH"),
		"existing content should be preserved, got: %s", content)
	assert.Assert(t, strings.Contains(content, "alias ll='ls -la'"),
		"existing content should be preserved, got: %s", content)
}

func TestCompletionUninstallNoBlockPresent(t *testing.T) {
	env := testenv.NewTestEnv(t)
	env.Extra["SHELL"] = "/bin/zsh"

	zshrc := filepath.Join(env.HomeDir, ".zshrc")
	original := "# just config\nexport BAR=baz\n"
	err := os.WriteFile(zshrc, []byte(original), 0o644)
	assert.NilError(t, err)

	result := binary.RunCLI(t, []string{"completion", "uninstall"}, env, env.HomeDir)
	assert.Equal(t, result.ExitCode, 0, "uninstall with no block should succeed")

	data, err := os.ReadFile(zshrc)
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(string(data), "export BAR=baz"),
		"content should be unchanged, got: %s", string(data))
}

func TestCompletionZshGeneratesScript(t *testing.T) {
	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{"completion", "zsh"}, env, env.HomeDir)
	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	assert.Assert(t, len(result.Stdout) > 0, "expected zsh completion output")
	assert.Assert(t, strings.Contains(result.Stdout, "compdef") || strings.Contains(result.Stdout, "#compdef"),
		"expected zsh completion markers in output, got: %s", result.Stdout[:min(200, len(result.Stdout))])
}

func TestCompletionBashGeneratesScript(t *testing.T) {
	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{"completion", "bash"}, env, env.HomeDir)
	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	assert.Assert(t, len(result.Stdout) > 0, "expected bash completion output")
	assert.Assert(t, strings.Contains(result.Stdout, "complete") || strings.Contains(result.Stdout, "bash_completion"),
		"expected bash completion markers in output, got: %s", result.Stdout[:min(200, len(result.Stdout))])
}

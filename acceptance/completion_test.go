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
	assert.Assert(t, strings.Contains(string(data), "completion.zsh"),
		"expected static file source line in .zshrc, got: %s", string(data))

	// Static completion file must exist and contain zsh completion content.
	completionFile := filepath.Join(env.HomeDir, ".config", "chunk", "completion.zsh")
	content, err := os.ReadFile(completionFile)
	assert.NilError(t, err, "expected static completion file at %s", completionFile)
	assert.Assert(t, strings.Contains(string(content), "compdef") || strings.Contains(string(content), "#compdef"),
		"expected zsh completion content in static file, got: %s", string(content)[:min(200, len(content))])
}

func TestCompletionInstallBash(t *testing.T) {
	env := testenv.NewTestEnv(t)
	env.Extra["SHELL"] = "/bin/bash"

	result := binary.RunCLI(t, []string{"completion", "install"}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	// Script must be at the XDG auto-discovery location.
	completionFile := filepath.Join(env.HomeDir, ".local", "share", "bash-completion", "completions", "chunk")
	content, err := os.ReadFile(completionFile)
	assert.NilError(t, err, "expected completion script at %s", completionFile)
	assert.Assert(t, len(content) > 0, "expected non-empty completion script")

	// No rc file modification for bash.
	for _, rc := range []string{".bashrc", ".bash_profile"} {
		_, statErr := os.Stat(filepath.Join(env.HomeDir, rc))
		assert.Assert(t, os.IsNotExist(statErr), "expected no rc file for bash, but %s exists", rc)
	}
}

func TestCompletionInstallBashIdempotent(t *testing.T) {
	env := testenv.NewTestEnv(t)
	env.Extra["SHELL"] = "/bin/bash"

	result := binary.RunCLI(t, []string{"completion", "install"}, env, env.HomeDir)
	assert.Equal(t, result.ExitCode, 0, "first install failed")

	result = binary.RunCLI(t, []string{"completion", "install"}, env, env.HomeDir)
	assert.Equal(t, result.ExitCode, 0, "second install failed")
	assert.Assert(t, strings.Contains(result.Stderr, "already installed"),
		"expected 'already installed' warning, got stderr: %s", result.Stderr)
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

func TestCompletionInstallBashNoRCModification(t *testing.T) {
	env := testenv.NewTestEnv(t)
	env.Extra["SHELL"] = "/bin/bash"

	result := binary.RunCLI(t, []string{"completion", "install"}, env, env.HomeDir)
	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	// bash uses XDG auto-discovery — no rc file should be created or modified.
	for _, rc := range []string{".bashrc", ".bash_profile"} {
		_, err := os.Stat(filepath.Join(env.HomeDir, rc))
		assert.Assert(t, os.IsNotExist(err), "expected no rc file modification for bash, but %s was created", rc)
	}
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

	completionFile := filepath.Join(env.HomeDir, ".config", "chunk", "completion.zsh")
	_, err = os.Stat(completionFile)
	assert.NilError(t, err, "expected static completion file to exist after install")

	// Uninstall
	result = binary.RunCLI(t, []string{"completion", "uninstall"}, env, env.HomeDir)
	assert.Equal(t, result.ExitCode, 0, "uninstall failed")

	// Verify rc file is clean
	data, err = os.ReadFile(zshrc)
	assert.NilError(t, err)
	assert.Assert(t, !strings.Contains(string(data), "# chunk shell completion"),
		"completion tag should be removed, got: %s", string(data))

	// Verify static file is removed
	_, err = os.Stat(completionFile)
	assert.Assert(t, os.IsNotExist(err), "expected static completion file to be removed after uninstall")
}

func TestCompletionUninstallBashRemovesScript(t *testing.T) {
	env := testenv.NewTestEnv(t)
	env.Extra["SHELL"] = "/bin/bash"

	result := binary.RunCLI(t, []string{"completion", "install"}, env, env.HomeDir)
	assert.Equal(t, result.ExitCode, 0, "install failed")

	scriptPath := filepath.Join(env.HomeDir, ".local", "share", "bash-completion", "completions", "chunk")
	_, err := os.Stat(scriptPath)
	assert.NilError(t, err, "expected script file after install")

	result = binary.RunCLI(t, []string{"completion", "uninstall"}, env, env.HomeDir)
	assert.Equal(t, result.ExitCode, 0, "uninstall failed")

	_, err = os.Stat(scriptPath)
	assert.Assert(t, os.IsNotExist(err), "expected script file to be removed after uninstall")
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

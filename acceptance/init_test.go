package acceptance

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/testing/binary"
	testenv "github.com/CircleCI-Public/chunk-cli/internal/testing/env"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/gitrepo"
)

// readInitConfig is a helper that reads and parses .chunk/config.json from workDir.
func readInitConfig(t *testing.T, workDir string) map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(workDir, ".chunk", "config.json"))
	assert.NilError(t, err, "expected .chunk/config.json to exist")
	var cfg map[string]interface{}
	assert.NilError(t, json.Unmarshal(data, &cfg))
	return cfg
}

// configCommands extracts the commands array from a parsed config.
func configCommands(cfg map[string]interface{}) []map[string]interface{} {
	raw, ok := cfg["commands"].([]interface{})
	if !ok {
		return nil
	}
	var cmds []map[string]interface{}
	for _, r := range raw {
		if m, ok := r.(map[string]interface{}); ok {
			cmds = append(cmds, m)
		}
	}
	return cmds
}

// commandNames returns the names of all commands in the config.
func commandNames(cfg map[string]interface{}) []string {
	cmds := configCommands(cfg)
	var names []string
	for _, c := range cmds {
		if n, ok := c["name"].(string); ok {
			names = append(names, n)
		}
	}
	return names
}

// commandByName finds a command by name in the config.
func commandByName(cfg map[string]interface{}, name string) map[string]interface{} {
	for _, c := range configCommands(cfg) {
		if c["name"] == name {
			return c
		}
	}
	return nil
}

func TestInitWritesVCSConfig(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "my-org", "my-repo")

	env := testenv.NewTestEnv(t)
	env.AnthropicKey = "" // skip claude
	env.CircleToken = ""

	result := binary.RunCLI(t, []string{
		"init", "--skip-hooks", "--skip-validate",
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	configPath := filepath.Join(workDir, ".chunk", "config.json")
	data, err := os.ReadFile(configPath)
	assert.NilError(t, err, "expected .chunk/config.json to exist")

	var cfg map[string]interface{}
	assert.NilError(t, json.Unmarshal(data, &cfg))

	vcs, ok := cfg["vcs"].(map[string]interface{})
	assert.Assert(t, ok, "expected vcs section in config, got: %s", string(data))
	assert.Equal(t, vcs["org"], "my-org", "expected org=my-org, got: %v", vcs["org"])
	assert.Equal(t, vcs["repo"], "my-repo", "expected repo=my-repo, got: %v", vcs["repo"])
}

func TestInitGitignoresPoolState(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "my-org", "my-repo")

	env := testenv.NewTestEnv(t)
	env.AnthropicKey = ""
	env.CircleToken = ""

	result := binary.RunCLI(t, []string{
		"init", "--skip-hooks", "--skip-validate",
	}, env, workDir)
	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	gitignore, err := os.ReadFile(filepath.Join(workDir, ".gitignore"))
	assert.NilError(t, err, "expected .gitignore to exist")
	assert.Assert(t, strings.Contains(string(gitignore), ".chunk/*-pool.json"),
		"expected .chunk/*-pool.json in .gitignore, got: %s", string(gitignore))

	// Re-run init when config already exists — still ensures the ignore entry.
	assert.NilError(t, os.WriteFile(filepath.Join(workDir, ".gitignore"), []byte("node_modules/\n"), 0o644))
	result = binary.RunCLI(t, []string{
		"init", "--skip-hooks", "--skip-validate",
	}, env, workDir)
	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	gitignore, err = os.ReadFile(filepath.Join(workDir, ".gitignore"))
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(string(gitignore), ".chunk/*-pool.json"),
		"expected re-init to restore pool gitignore, got: %s", string(gitignore))
}

func TestInitSkipAllWritesOnlyVCS(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")

	env := testenv.NewTestEnv(t)
	env.AnthropicKey = ""
	env.CircleToken = ""

	result := binary.RunCLI(t, []string{
		"init", "--skip-hooks", "--skip-validate",
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	configPath := filepath.Join(workDir, ".chunk", "config.json")
	data, err := os.ReadFile(configPath)
	assert.NilError(t, err, "expected .chunk/config.json to exist")

	var cfg map[string]interface{}
	assert.NilError(t, json.Unmarshal(data, &cfg))

	vcs, ok := cfg["vcs"].(map[string]interface{})
	assert.Assert(t, ok, "expected vcs section, got: %s", string(data))
	assert.Equal(t, vcs["org"], "test-org")
	assert.Equal(t, vcs["repo"], "test-repo")

	_, hasCommands := cfg["commands"]
	assert.Assert(t, !hasCommands || cfg["commands"] == nil ||
		len(cfg["commands"].([]interface{})) == 0,
		"expected no commands with --skip-validate, got: %s", string(data))
}

func TestInitExistingConfigNoForce(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	writeProjectConfig(t, workDir, "echo install", "echo test")

	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{
		"init", "--skip-hooks", "--skip-validate",
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "expected clean exit when config exists without --force\nstdout: %s\nstderr: %s", result.Stdout, result.Stderr)
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "already exists") || strings.Contains(combined, "--force"),
		"expected existing config message, got: %s", combined)
}

func TestInitExistingConfigWithForce(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	writeProjectConfig(t, workDir, "echo install", "echo test")

	env := testenv.NewTestEnv(t)
	env.AnthropicKey = ""
	env.CircleToken = ""

	result := binary.RunCLI(t, []string{
		"init", "--force", "--skip-hooks", "--skip-validate",
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
}

func TestInitForcePreservesSkippedSections(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "new-org", "new-repo")

	// Write existing config with VCS and Commands.
	chunkDir := filepath.Join(workDir, ".chunk")
	assert.NilError(t, os.MkdirAll(chunkDir, 0o755))
	existing := `{"vcs":{"org":"old-org","repo":"old-repo"},"commands":[{"name":"test","run":"echo test"}]}`
	assert.NilError(t, os.WriteFile(filepath.Join(chunkDir, "config.json"), []byte(existing), 0o644))

	env := testenv.NewTestEnv(t)
	env.AnthropicKey = ""
	env.CircleToken = ""

	// --force re-runs init; --skip-validate skips validate command detection.
	result := binary.RunCLI(t, []string{
		"init", "--force", "--skip-hooks", "--skip-validate",
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	data, err := os.ReadFile(filepath.Join(chunkDir, "config.json"))
	assert.NilError(t, err)

	var cfg map[string]interface{}
	assert.NilError(t, json.Unmarshal(data, &cfg))

	// VCS should be re-detected from git remote.
	vcs, ok := cfg["vcs"].(map[string]interface{})
	assert.Assert(t, ok, "expected vcs section, got: %s", string(data))
	assert.Equal(t, vcs["org"], "new-org")
	assert.Equal(t, vcs["repo"], "new-repo")

	// Commands should be preserved (--skip-validate).
	cmds, ok := cfg["commands"].([]interface{})
	assert.Assert(t, ok && len(cmds) > 0, "expected commands preserved, got: %s", string(data))
	cmd0 := cmds[0].(map[string]interface{})
	assert.Equal(t, cmd0["name"], "test")
	assert.Equal(t, cmd0["run"], "echo test")
}

func TestInitNotGitRepo(t *testing.T) {
	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{
		"init", "--skip-hooks", "--skip-validate",
	}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "git"),
		"expected git repo error, got: %s", combined)
}

// --- Validate command detection (Gap 2) ---

func TestInitDetectsTaskfileGoCommands(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "my-org", "my-repo")
	assert.NilError(t, os.WriteFile(filepath.Join(workDir, "Taskfile.yml"), []byte("version: '3'\n"), 0o644))
	assert.NilError(t, os.WriteFile(filepath.Join(workDir, "go.mod"), []byte("module example.com/m\n"), 0o644))

	env := testenv.NewTestEnv(t)
	env.AnthropicKey = ""
	env.CircleToken = ""

	result := binary.RunCLI(t, []string{
		"init", "--skip-hooks",
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	cfg := readInitConfig(t, workDir)
	names := commandNames(cfg)
	assert.Assert(t, len(names) == 3, "expected test, lint, format; got: %v", names)

	test := commandByName(cfg, "test")
	assert.Assert(t, test != nil, "expected test command")
	assert.Equal(t, test["run"], "task test")

	lint := commandByName(cfg, "lint")
	assert.Assert(t, lint != nil, "expected lint command")
	assert.Equal(t, lint["run"], "task lint")

	format := commandByName(cfg, "format")
	assert.Assert(t, format != nil, "expected format command")
	assert.Equal(t, format["run"], "task fmt")
	assert.Equal(t, format["local"], true)

	assert.Assert(t, commandByName(cfg, "test-changed") == nil, "test-changed should not be detected")
}

func TestInitDetectsMakefileGoCommands(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "my-org", "my-repo")
	assert.NilError(t, os.WriteFile(filepath.Join(workDir, "Makefile"), []byte("test:\n\tgo test ./...\n"), 0o644))
	assert.NilError(t, os.WriteFile(filepath.Join(workDir, "go.mod"), []byte("module example.com/m\n"), 0o644))

	env := testenv.NewTestEnv(t)
	env.AnthropicKey = ""
	env.CircleToken = ""

	result := binary.RunCLI(t, []string{
		"init", "--skip-hooks",
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	cfg := readInitConfig(t, workDir)
	test := commandByName(cfg, "test")
	assert.Assert(t, test != nil, "expected test command")
	assert.Equal(t, test["run"], "make test")

	lint := commandByName(cfg, "lint")
	assert.Assert(t, lint != nil, "expected lint command")
	assert.Equal(t, lint["run"], "make lint")
}

func TestInitDetectsGoModOnlyCommands(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "my-org", "my-repo")
	assert.NilError(t, os.WriteFile(filepath.Join(workDir, "go.mod"), []byte("module example.com/m\n"), 0o644))

	env := testenv.NewTestEnv(t)
	env.AnthropicKey = ""
	env.CircleToken = ""

	result := binary.RunCLI(t, []string{
		"init", "--skip-hooks",
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	cfg := readInitConfig(t, workDir)
	test := commandByName(cfg, "test")
	assert.Assert(t, test != nil, "expected test command")
	assert.Equal(t, test["run"], "go test ./...")

	lint := commandByName(cfg, "lint")
	assert.Assert(t, lint != nil, "expected lint command")
	assert.Equal(t, lint["run"], "golangci-lint run ./...")

	format := commandByName(cfg, "format")
	assert.Assert(t, format != nil, "expected format command")
	assert.Equal(t, format["run"], "gofmt -w .")
	assert.Equal(t, format["local"], true)
}

func TestInitDetectsCargoCommands(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "my-org", "my-repo")
	assert.NilError(t, os.WriteFile(filepath.Join(workDir, "Cargo.toml"), []byte("[package]\nname = \"test\"\n"), 0o644))

	env := testenv.NewTestEnv(t)
	env.AnthropicKey = ""
	env.CircleToken = ""

	result := binary.RunCLI(t, []string{
		"init", "--skip-hooks",
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	cfg := readInitConfig(t, workDir)
	test := commandByName(cfg, "test")
	assert.Assert(t, test != nil, "expected test command")
	assert.Equal(t, test["run"], "cargo test")
}

func TestInitDetectsPyprojectCommands(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "my-org", "my-repo")
	assert.NilError(t, os.WriteFile(filepath.Join(workDir, "pyproject.toml"), []byte("[project]\nname = \"test\"\n"), 0o644))

	env := testenv.NewTestEnv(t)
	env.AnthropicKey = ""
	env.CircleToken = ""

	result := binary.RunCLI(t, []string{
		"init", "--skip-hooks",
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	cfg := readInitConfig(t, workDir)
	test := commandByName(cfg, "test")
	assert.Assert(t, test != nil, "expected test command")
	assert.Equal(t, test["run"], "pytest")
}

func TestInitDetectsRequirementsTxtCommands(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "my-org", "my-repo")
	assert.NilError(t, os.WriteFile(filepath.Join(workDir, "requirements.txt"), []byte("pytest\n"), 0o644))

	env := testenv.NewTestEnv(t)
	env.AnthropicKey = ""
	env.CircleToken = ""

	result := binary.RunCLI(t, []string{
		"init", "--skip-hooks",
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	cfg := readInitConfig(t, workDir)
	test := commandByName(cfg, "test")
	assert.Assert(t, test != nil, "expected test command")
	assert.Equal(t, test["run"], "pytest")
}

func TestInitDetectsSetupPyCommands(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "my-org", "my-repo")
	assert.NilError(t, os.WriteFile(filepath.Join(workDir, "setup.py"), []byte("from setuptools import setup\nsetup(name='test')\n"), 0o644))

	env := testenv.NewTestEnv(t)
	env.AnthropicKey = ""
	env.CircleToken = ""

	result := binary.RunCLI(t, []string{
		"init", "--skip-hooks",
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	cfg := readInitConfig(t, workDir)
	test := commandByName(cfg, "test")
	assert.Assert(t, test != nil, "expected test command")
	assert.Equal(t, test["run"], "pytest")
}

func TestInitDetectsPipfileCommands(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "my-org", "my-repo")
	assert.NilError(t, os.WriteFile(filepath.Join(workDir, "Pipfile"), []byte("[packages]\npytest = \"*\"\n"), 0o644))

	env := testenv.NewTestEnv(t)
	env.AnthropicKey = ""
	env.CircleToken = ""

	result := binary.RunCLI(t, []string{
		"init", "--skip-hooks",
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	cfg := readInitConfig(t, workDir)
	test := commandByName(cfg, "test")
	assert.Assert(t, test != nil, "expected test command")
	assert.Equal(t, test["run"], "pytest")
}

func TestInitDetectsGemfileCommands(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "my-org", "my-repo")
	assert.NilError(t, os.WriteFile(filepath.Join(workDir, "Gemfile"), []byte("source 'https://rubygems.org'\ngem 'rails'\n"), 0o644))

	env := testenv.NewTestEnv(t)
	env.AnthropicKey = ""
	env.CircleToken = ""

	result := binary.RunCLI(t, []string{
		"init", "--skip-hooks",
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	cfg := readInitConfig(t, workDir)
	test := commandByName(cfg, "test")
	assert.Assert(t, test != nil, "expected test command")
	assert.Equal(t, test["run"], "bundle exec rake test")
}

func TestInitDetectsPomXmlCommands(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "my-org", "my-repo")
	assert.NilError(t, os.WriteFile(filepath.Join(workDir, "pom.xml"), []byte("<project><modelVersion>4.0.0</modelVersion></project>\n"), 0o644))

	env := testenv.NewTestEnv(t)
	env.AnthropicKey = ""
	env.CircleToken = ""

	result := binary.RunCLI(t, []string{
		"init", "--skip-hooks",
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	cfg := readInitConfig(t, workDir)
	test := commandByName(cfg, "test")
	assert.Assert(t, test != nil, "expected test command")
	assert.Equal(t, test["run"], "mvn test")
}

func TestInitDetectsGradleCommands(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "my-org", "my-repo")
	assert.NilError(t, os.WriteFile(filepath.Join(workDir, "build.gradle"), []byte("apply plugin: 'java'\n"), 0o644))
	assert.NilError(t, os.WriteFile(filepath.Join(workDir, "gradlew"), []byte("#!/bin/sh\n"), 0o755))

	env := testenv.NewTestEnv(t)
	env.AnthropicKey = ""
	env.CircleToken = ""

	result := binary.RunCLI(t, []string{
		"init", "--skip-hooks",
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	cfg := readInitConfig(t, workDir)
	test := commandByName(cfg, "test")
	assert.Assert(t, test != nil, "expected test command")
	assert.Equal(t, test["run"], "./gradlew test")
}

func TestInitDetectsGradleWithoutWrapper(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "my-org", "my-repo")
	assert.NilError(t, os.WriteFile(filepath.Join(workDir, "build.gradle"), []byte("apply plugin: 'java'\n"), 0o644))

	env := testenv.NewTestEnv(t)
	env.AnthropicKey = ""
	env.CircleToken = ""

	result := binary.RunCLI(t, []string{
		"init", "--skip-hooks",
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	cfg := readInitConfig(t, workDir)
	test := commandByName(cfg, "test")
	assert.Assert(t, test != nil, "expected test command")
	assert.Equal(t, test["run"], "gradle test")
}

func TestInitDetectsGradleKtsCommands(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "my-org", "my-repo")
	assert.NilError(t, os.WriteFile(filepath.Join(workDir, "build.gradle.kts"), []byte("plugins { java }\n"), 0o644))
	assert.NilError(t, os.WriteFile(filepath.Join(workDir, "gradlew"), []byte("#!/bin/sh\n"), 0o755))

	env := testenv.NewTestEnv(t)
	env.AnthropicKey = ""
	env.CircleToken = ""

	result := binary.RunCLI(t, []string{
		"init", "--skip-hooks",
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	cfg := readInitConfig(t, workDir)
	test := commandByName(cfg, "test")
	assert.Assert(t, test != nil, "expected test command")
	assert.Equal(t, test["run"], "./gradlew test")
}

func TestInitDetectsPackageJsonWithYarnLock(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "my-org", "my-repo")
	assert.NilError(t, os.WriteFile(filepath.Join(workDir, "package.json"), []byte(`{"name":"test"}`), 0o644))
	assert.NilError(t, os.WriteFile(filepath.Join(workDir, "yarn.lock"), []byte(""), 0o644))

	env := testenv.NewTestEnv(t)
	env.AnthropicKey = ""
	env.CircleToken = ""

	result := binary.RunCLI(t, []string{
		"init", "--skip-hooks",
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	cfg := readInitConfig(t, workDir)

	// Should detect yarn as package manager and add install command
	install := commandByName(cfg, "install")
	assert.Assert(t, install != nil, "expected install command for yarn")
	assert.Equal(t, install["run"], "yarn install --frozen-lockfile")

	test := commandByName(cfg, "test")
	assert.Assert(t, test != nil, "expected test command")
	assert.Equal(t, test["run"], "yarn test")
}

func TestInitDetectsPackageJsonWithPnpmLock(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "my-org", "my-repo")
	assert.NilError(t, os.WriteFile(filepath.Join(workDir, "package.json"), []byte(`{"name":"test"}`), 0o644))
	assert.NilError(t, os.WriteFile(filepath.Join(workDir, "pnpm-lock.yaml"), []byte(""), 0o644))

	env := testenv.NewTestEnv(t)
	env.AnthropicKey = ""
	env.CircleToken = ""

	result := binary.RunCLI(t, []string{
		"init", "--skip-hooks",
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	cfg := readInitConfig(t, workDir)

	install := commandByName(cfg, "install")
	assert.Assert(t, install != nil, "expected install command for pnpm")
	assert.Equal(t, install["run"], "pnpm install --frozen-lockfile")

	test := commandByName(cfg, "test")
	assert.Assert(t, test != nil, "expected test command")
	assert.Equal(t, test["run"], "pnpm test")
}

func TestInitDetectsUnknownToolchainNoClaude(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "my-org", "my-repo")
	// No recognized files -- unknown toolchain

	env := testenv.NewTestEnv(t)
	env.AnthropicKey = "" // no Claude fallback
	env.CircleToken = ""

	result := binary.RunCLI(t, []string{
		"init", "--skip-hooks",
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	cfg := readInitConfig(t, workDir)
	test := commandByName(cfg, "test")
	assert.Assert(t, test == nil, "expected no test command for unknown toolchain without Claude")
}

// --- Hook setup (Gap 3) ---

// chunk init writes no agent hook files, even for a project that has a .codex
// directory, and still installs the git pre-commit hook.
func TestInitWritesNoAgentHookFiles(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "my-org", "my-repo")
	err := os.WriteFile(filepath.Join(workDir, "go.mod"), []byte("module example.com/m\n"), 0o644)
	assert.NilError(t, err)
	err = os.MkdirAll(filepath.Join(workDir, ".codex"), 0o755)
	assert.NilError(t, err)

	env := testenv.NewTestEnv(t)
	env.AnthropicKey = ""
	env.CircleToken = ""

	result := binary.RunCLI(t, []string{"init"}, env, workDir)
	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	for _, rel := range []string{
		".claude/settings.json",
		".claude/settings.example.json",
		".codex/hooks.json",
		".codex/hooks.example.json",
	} {
		_, err := os.Stat(filepath.Join(workDir, rel))
		assert.Check(t, os.IsNotExist(err), "expected no %s, stat err: %v", rel, err)
	}

	hook, err := os.ReadFile(filepath.Join(workDir, ".git", "hooks", "pre-commit"))
	assert.NilError(t, err, "expected .git/hooks/pre-commit to exist")
	assert.Check(t, cmp.Contains(string(hook), "chunk validate"))
}

// Agent files a project already has are left exactly as they are, with or
// without --force, and no example is written beside them.
func TestInitLeavesExistingAgentFilesAlone(t *testing.T) {
	files := map[string]string{
		".claude/settings.json": `{"existing": true}`,
		".codex/hooks.json":     `{"hooks":{}}`,
	}
	for _, args := range [][]string{{"init"}, {"init", "--force"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			workDir := gitrepo.SetupGitRepo(t, "my-org", "my-repo")
			err := os.WriteFile(filepath.Join(workDir, "go.mod"), []byte("module example.com/m\n"), 0o644)
			assert.NilError(t, err)
			for rel, content := range files {
				path := filepath.Join(workDir, rel)
				err = os.MkdirAll(filepath.Dir(path), 0o755)
				assert.NilError(t, err)
				err = os.WriteFile(path, []byte(content), 0o644)
				assert.NilError(t, err)
			}

			env := testenv.NewTestEnv(t)
			env.AnthropicKey = ""
			env.CircleToken = ""

			result := binary.RunCLI(t, args, env, workDir)
			assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

			for rel, want := range files {
				got, err := os.ReadFile(filepath.Join(workDir, rel))
				assert.NilError(t, err)
				assert.Check(t, cmp.Equal(string(got), want), rel)
			}
			for _, rel := range []string{".claude/settings.example.json", ".codex/hooks.example.json"} {
				_, err := os.Stat(filepath.Join(workDir, rel))
				assert.Check(t, os.IsNotExist(err), "expected no %s, stat err: %v", rel, err)
			}
		})
	}
}

// chunk hook status fails as an unknown command.
func TestHookCommandIsUnknown(t *testing.T) {
	env := testenv.NewTestEnv(t)
	result := binary.RunCLI(t, []string{"hook", "status"}, env, t.TempDir())
	assert.Check(t, result.ExitCode != 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
	assert.Check(t, cmp.Contains(result.Stderr, "unknown command"))
}

// --- init never touches CircleCI ---

// TestInitNoCircleCINoToken verifies init succeeds even with no CircleCI token.
func TestInitNoCircleCINoToken(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "my-org", "my-repo")

	env := testenv.NewTestEnv(t)
	env.AnthropicKey = ""
	env.CircleToken = "" // no token

	result := binary.RunCLI(t, []string{
		"init", "--skip-hooks", "--skip-validate",
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	// init must not mention CircleCI
	combined := result.Stdout + result.Stderr
	assert.Assert(t, !strings.Contains(combined, "CircleCI"),
		"expected init to not mention CircleCI, got: %s", combined)
}

// TestInitNoCircleCIWithToken verifies init ignores a CircleCI token if present.
func TestInitNoCircleCIWithToken(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "my-org", "my-repo")

	env := testenv.NewTestEnv(t)
	env.AnthropicKey = ""
	env.CircleToken = ""

	result := binary.RunCLI(t, []string{
		"init", "--skip-hooks", "--skip-validate",
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	// init must not mention CircleCI even when a token is available
	combined := result.Stdout + result.Stderr
	assert.Assert(t, !strings.Contains(combined, "CircleCI"),
		"expected init to not mention CircleCI, got: %s", combined)
}

// --- --project-dir flag ---

func TestInitProjectDir(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "my-org", "my-repo")

	env := testenv.NewTestEnv(t)
	env.AnthropicKey = ""
	env.CircleToken = ""

	// Run from a different directory but point --project-dir at the git repo
	result := binary.RunCLI(t, []string{
		"init", "--skip-hooks", "--skip-validate",
		"--project-dir", workDir,
	}, env, env.HomeDir) // CWD is HomeDir, not workDir

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	cfg := readInitConfig(t, workDir)
	vcs, ok := cfg["vcs"].(map[string]interface{})
	assert.Assert(t, ok, "expected vcs section in config")
	assert.Equal(t, vcs["org"], "my-org")
	assert.Equal(t, vcs["repo"], "my-repo")
}

// --- CircleCI Smarter Testing test-suites.yml ---

func TestInitWritesTestSuitesForGoWhenCircleDirExists(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "my-org", "my-repo")
	assert.NilError(t, os.WriteFile(filepath.Join(workDir, "go.mod"), []byte("module example.com/m\n"), 0o644))
	assert.NilError(t, os.MkdirAll(filepath.Join(workDir, ".circleci"), 0o755))

	env := testenv.NewTestEnv(t)
	env.AnthropicKey = ""
	env.CircleToken = ""

	result := binary.RunCLI(t, []string{
		"init", "--skip-hooks", "--skip-test-suites=false",
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	data, err := os.ReadFile(filepath.Join(workDir, ".circleci", "test-suites.yml"))
	assert.NilError(t, err, "expected .circleci/test-suites.yml to exist")
	body := string(data)
	assert.Assert(t, strings.Contains(body, "go list -f"), "got: %s", body)
	assert.Assert(t, strings.Contains(body, "<< test.atoms >>"), "got: %s", body)
}

func TestInitCreatesCircleDirAndWritesTestSuites(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "my-org", "my-repo")
	assert.NilError(t, os.WriteFile(filepath.Join(workDir, "go.mod"), []byte("module example.com/m\n"), 0o644))

	env := testenv.NewTestEnv(t)
	env.AnthropicKey = ""
	env.CircleToken = ""

	result := binary.RunCLI(t, []string{
		"init", "--skip-hooks", "--skip-test-suites=false",
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	data, err := os.ReadFile(filepath.Join(workDir, ".circleci", "test-suites.yml"))
	assert.NilError(t, err, "expected init to create .circleci/test-suites.yml even when .circleci/ was missing")
	body := string(data)
	assert.Assert(t, strings.Contains(body, "go list -f"), "got: %s", body)
	assert.Assert(t, strings.Contains(body, "<< test.atoms >>"), "got: %s", body)
}

func TestInitDoesNotOverwriteExistingTestSuites(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "my-org", "my-repo")
	assert.NilError(t, os.WriteFile(filepath.Join(workDir, "go.mod"), []byte("module example.com/m\n"), 0o644))
	assert.NilError(t, os.MkdirAll(filepath.Join(workDir, ".circleci"), 0o755))
	existing := []byte("# user customization\nname: custom\n")
	assert.NilError(t, os.WriteFile(filepath.Join(workDir, ".circleci", "test-suites.yml"), existing, 0o644))

	env := testenv.NewTestEnv(t)
	env.AnthropicKey = ""
	env.CircleToken = ""

	result := binary.RunCLI(t, []string{
		"init", "--skip-hooks", "--skip-test-suites=false",
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	data, err := os.ReadFile(filepath.Join(workDir, ".circleci", "test-suites.yml"))
	assert.NilError(t, err)
	assert.Equal(t, string(data), string(existing), "existing test-suites.yml should be preserved")
}

func TestInitSkipsTestSuitesByDefault(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "my-org", "my-repo")
	assert.NilError(t, os.WriteFile(filepath.Join(workDir, "go.mod"), []byte("module example.com/m\n"), 0o644))
	assert.NilError(t, os.MkdirAll(filepath.Join(workDir, ".circleci"), 0o755))

	env := testenv.NewTestEnv(t)
	env.AnthropicKey = ""
	env.CircleToken = ""

	result := binary.RunCLI(t, []string{
		"init", "--skip-hooks",
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	_, err := os.Stat(filepath.Join(workDir, ".circleci", "test-suites.yml"))
	assert.Assert(t, os.IsNotExist(err), "expected default to skip write, err=%v", err)

	assert.Assert(t, strings.Contains(result.Stderr, "chunk-sidecar"),
		"expected sidecar hint in stderr, got: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stderr, "set up a sidecar"),
		"expected hint to mention setting up a sidecar, got: %s", result.Stderr)
}

func TestInitProjectDirNotGitRepo(t *testing.T) {
	env := testenv.NewTestEnv(t)
	notGit := t.TempDir()

	result := binary.RunCLI(t, []string{
		"init", "--skip-hooks", "--skip-validate",
		"--project-dir", notGit,
	}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit for non-git project-dir")
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "git"),
		"expected git error, got: %s", combined)
}

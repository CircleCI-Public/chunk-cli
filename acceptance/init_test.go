package acceptance

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/testing/binary"
	testenv "github.com/CircleCI-Public/chunk-cli/internal/testing/env"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
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

	cci := fakes.NewFakeCircleCI()
	cci.Collaborations = []fakes.Collaboration{
		{ID: "org-aaa", Name: "my-org"},
	}
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL
	env.AnthropicKey = "" // skip claude

	result := binary.RunCLI(t, []string{
		"init", "--skip-hooks", "--skip-validate", "--skip-circleci",
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

func TestInitSkipAllWritesOnlyVCS(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")

	env := testenv.NewTestEnv(t)
	env.AnthropicKey = ""

	result := binary.RunCLI(t, []string{
		"init", "--skip-hooks", "--skip-validate", "--skip-circleci",
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
		"init", "--skip-hooks", "--skip-validate", "--skip-circleci",
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

	result := binary.RunCLI(t, []string{
		"init", "--force", "--skip-hooks", "--skip-validate", "--skip-circleci",
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
}

func TestInitForcePreservesSkippedSections(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "new-org", "new-repo")

	// Write existing config with VCS, CircleCI, and Commands.
	chunkDir := filepath.Join(workDir, ".chunk")
	assert.NilError(t, os.MkdirAll(chunkDir, 0o755))
	existing := `{"vcs":{"org":"old-org","repo":"old-repo"},"circleci":{"orgId":"abc-123"},"commands":[{"name":"test","run":"echo test"}]}`
	assert.NilError(t, os.WriteFile(filepath.Join(chunkDir, "config.json"), []byte(existing), 0o644))

	env := testenv.NewTestEnv(t)
	env.AnthropicKey = ""

	// --force re-runs init; --skip-circleci and --skip-validate skip those sections.
	result := binary.RunCLI(t, []string{
		"init", "--force", "--skip-hooks", "--skip-validate", "--skip-circleci",
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

	// CircleCI should be preserved (--skip-circleci).
	cci, ok := cfg["circleci"].(map[string]interface{})
	assert.Assert(t, ok, "expected circleci section preserved, got: %s", string(data))
	assert.Equal(t, cci["orgId"], "abc-123")

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
		"init", "--skip-hooks", "--skip-validate", "--skip-circleci",
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

	result := binary.RunCLI(t, []string{
		"init", "--skip-hooks", "--skip-circleci",
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

	assert.Assert(t, commandByName(cfg, "test-changed") == nil, "test-changed should not be detected")
}

func TestInitDetectsMakefileGoCommands(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "my-org", "my-repo")
	assert.NilError(t, os.WriteFile(filepath.Join(workDir, "Makefile"), []byte("test:\n\tgo test ./...\n"), 0o644))
	assert.NilError(t, os.WriteFile(filepath.Join(workDir, "go.mod"), []byte("module example.com/m\n"), 0o644))

	env := testenv.NewTestEnv(t)
	env.AnthropicKey = ""

	result := binary.RunCLI(t, []string{
		"init", "--skip-hooks", "--skip-circleci",
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

	result := binary.RunCLI(t, []string{
		"init", "--skip-hooks", "--skip-circleci",
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
}

func TestInitDetectsCargoCommands(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "my-org", "my-repo")
	assert.NilError(t, os.WriteFile(filepath.Join(workDir, "Cargo.toml"), []byte("[package]\nname = \"test\"\n"), 0o644))

	env := testenv.NewTestEnv(t)
	env.AnthropicKey = ""

	result := binary.RunCLI(t, []string{
		"init", "--skip-hooks", "--skip-circleci",
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

	result := binary.RunCLI(t, []string{
		"init", "--skip-hooks", "--skip-circleci",
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	cfg := readInitConfig(t, workDir)
	test := commandByName(cfg, "test")
	assert.Assert(t, test != nil, "expected test command")
	assert.Equal(t, test["run"], "pytest")
}

func TestInitDetectsPackageJsonWithYarnLock(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "my-org", "my-repo")
	assert.NilError(t, os.WriteFile(filepath.Join(workDir, "package.json"), []byte(`{"name":"test"}`), 0o644))
	assert.NilError(t, os.WriteFile(filepath.Join(workDir, "yarn.lock"), []byte(""), 0o644))

	env := testenv.NewTestEnv(t)
	env.AnthropicKey = ""

	result := binary.RunCLI(t, []string{
		"init", "--skip-hooks", "--skip-circleci",
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

	result := binary.RunCLI(t, []string{
		"init", "--skip-hooks", "--skip-circleci",
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

	result := binary.RunCLI(t, []string{
		"init", "--skip-hooks", "--skip-circleci",
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	cfg := readInitConfig(t, workDir)
	test := commandByName(cfg, "test")
	assert.Assert(t, test != nil, "expected fallback test command")
	assert.Equal(t, test["run"], "npm test", "expected npm test fallback for unknown toolchain")
}

// --- Hook setup (Gap 3) ---

func TestInitCreatesHookFiles(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "my-org", "my-repo")
	assert.NilError(t, os.WriteFile(filepath.Join(workDir, "go.mod"), []byte("module example.com/m\n"), 0o644))

	env := testenv.NewTestEnv(t)
	env.AnthropicKey = ""

	result := binary.RunCLI(t, []string{
		"init", "--skip-circleci",
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	// .claude/settings.json should exist with hooks
	settingsPath := filepath.Join(workDir, ".claude", "settings.json")
	data, err := os.ReadFile(settingsPath)
	assert.NilError(t, err, "expected .claude/settings.json to exist")

	settingsContent := string(data)
	assert.Assert(t, strings.Contains(settingsContent, "hooks"),
		"expected hooks section in settings.json, got: %s", settingsContent)
}

func TestInitHookExistingSettingsForceWritesExample(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "my-org", "my-repo")
	assert.NilError(t, os.WriteFile(filepath.Join(workDir, "go.mod"), []byte("module example.com/m\n"), 0o644))

	// Pre-create .claude/settings.json
	claudeDir := filepath.Join(workDir, ".claude")
	assert.NilError(t, os.MkdirAll(claudeDir, 0o755))
	assert.NilError(t, os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(`{"existing": true}`), 0o644))

	env := testenv.NewTestEnv(t)
	env.AnthropicKey = ""

	result := binary.RunCLI(t, []string{
		"init", "--skip-circleci", "--force",
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	// settings.json is scaffold-once: even with --force, existing file is preserved
	// and an example is written instead
	data, err := os.ReadFile(filepath.Join(claudeDir, "settings.json"))
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(string(data), "existing"),
		"expected settings.json to be preserved, got: %s", string(data))

	exampleData, err := os.ReadFile(filepath.Join(claudeDir, "settings.example.json"))
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(string(exampleData), "hooks"),
		"expected example to contain hooks, got: %s", string(exampleData))
}

func TestInitHookExistingSettingsWritesExample(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "my-org", "my-repo")
	assert.NilError(t, os.WriteFile(filepath.Join(workDir, "go.mod"), []byte("module example.com/m\n"), 0o644))

	// Pre-create .claude/settings.json
	claudeDir := filepath.Join(workDir, ".claude")
	assert.NilError(t, os.MkdirAll(claudeDir, 0o755))
	assert.NilError(t, os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(`{"existing": true}`), 0o644))

	env := testenv.NewTestEnv(t)
	env.AnthropicKey = ""

	// Without --force on config, but config doesn't exist yet so init proceeds.
	// However settings.json already exists, so hook setup writes .example.
	result := binary.RunCLI(t, []string{
		"init", "--skip-circleci",
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	// Original settings.json should be unchanged
	data, err := os.ReadFile(filepath.Join(claudeDir, "settings.json"))
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(string(data), "existing"),
		"expected original settings.json to be preserved without --force")

	// Example should exist
	examplePath := filepath.Join(claudeDir, "settings.example.json")
	_, err = os.Stat(examplePath)
	assert.NilError(t, err, "expected settings.example.json to exist")
}

// --- CircleCI org picker (Gap 1) ---

func TestInitCircleCISingleOrgAutoSelect(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "my-org", "my-repo")

	cci := fakes.NewFakeCircleCI()
	cci.Collaborations = []fakes.Collaboration{
		{ID: "org-aaa", Name: "my-org"},
	}
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL
	env.AnthropicKey = ""

	// With a single org, the command should auto-select without prompting.
	result := binary.RunCLI(t, []string{
		"init", "--skip-hooks", "--skip-validate",
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	cfg := readInitConfig(t, workDir)

	cciCfg, ok := cfg["circleci"].(map[string]interface{})
	assert.Assert(t, ok, "expected circleci config when single org is available")
	assert.Equal(t, cciCfg["orgId"], "org-aaa")
}

func TestInitCircleCINoToken(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "my-org", "my-repo")

	env := testenv.NewTestEnv(t)
	env.CircleToken = "" // no token
	env.AnthropicKey = ""

	result := binary.RunCLI(t, []string{
		"init", "--skip-hooks", "--skip-validate",
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(strings.ToLower(combined), "skip"),
		"expected skip warning when no CIRCLE_TOKEN, got: %s", combined)
}

func TestInitCircleCIZeroOrgs(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "my-org", "my-repo")

	cci := fakes.NewFakeCircleCI()
	cci.Collaborations = []fakes.Collaboration{} // zero orgs
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL
	env.AnthropicKey = ""

	result := binary.RunCLI(t, []string{
		"init", "--skip-hooks", "--skip-validate",
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "No CircleCI organizations"),
		"expected zero-org warning, got: %s", combined)
}

// --- --project-dir flag ---

func TestInitProjectDir(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "my-org", "my-repo")

	env := testenv.NewTestEnv(t)
	env.AnthropicKey = ""

	// Run from a different directory but point --project-dir at the git repo
	result := binary.RunCLI(t, []string{
		"init", "--skip-hooks", "--skip-validate", "--skip-circleci",
		"--project-dir", workDir,
	}, env, env.HomeDir) // CWD is HomeDir, not workDir

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	cfg := readInitConfig(t, workDir)
	vcs, ok := cfg["vcs"].(map[string]interface{})
	assert.Assert(t, ok, "expected vcs section in config")
	assert.Equal(t, vcs["org"], "my-org")
	assert.Equal(t, vcs["repo"], "my-repo")
}

func TestInitProjectDirNotGitRepo(t *testing.T) {
	env := testenv.NewTestEnv(t)
	notGit := t.TempDir()

	result := binary.RunCLI(t, []string{
		"init", "--skip-hooks", "--skip-validate", "--skip-circleci",
		"--project-dir", notGit,
	}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit for non-git project-dir")
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "git"),
		"expected git error, got: %s", combined)
}

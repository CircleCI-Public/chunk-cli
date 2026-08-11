package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

func fakeConfirmYes(_ string, _ bool) (bool, error) { return true, nil }
func fakeConfirmNo(_ string, _ bool) (bool, error)  { return false, nil }
func fakeConfirmErr(_ string, _ bool) (bool, error) {
	return false, errors.New("no tty")
}

func testStreams() (iostream.Streams, *bytes.Buffer, *bytes.Buffer) {
	var out, errOut bytes.Buffer
	return iostream.Streams{Out: &out, Err: &errOut}, &out, &errOut
}

func testCfg(validateCmds ...config.ValidateCommand) *config.ProjectConfig {
	return &config.ProjectConfig{Validate: validateCmds}
}

func TestWriteSettingsNewFile(t *testing.T) {
	dir := t.TempDir()
	streams, _, errOut := testStreams()

	cfg := testCfg(config.ValidateCommand{Name: "test", Run: "go test ./...", Timeout: 60})

	err := writeSettings(dir, cfg, streams, fakeConfirmYes)
	assert.NilError(t, err)

	data, err := os.ReadFile(filepath.Join(dir, ".claude", "settings.json"))
	assert.NilError(t, err)

	var parsed map[string]interface{}
	assert.NilError(t, json.Unmarshal(data, &parsed))
	assert.Equal(t, parsed["$schema"], "https://json.schemastore.org/claude-code-settings.json")
	assert.Assert(t, errOut.Len() > 0)
}

func TestWriteSettingsExistingMergeApplied(t *testing.T) {
	dir := t.TempDir()
	claudeDir := filepath.Join(dir, ".claude")
	assert.NilError(t, os.MkdirAll(claudeDir, 0o755))

	existing := []byte(`{
  "env": {"MY_CUSTOM_VAR": "hello"},
  "permissions": {"allow": ["Read"]}
}`)
	assert.NilError(t, os.WriteFile(filepath.Join(claudeDir, "settings.json"), existing, 0o644))

	streams, _, errOut := testStreams()
	cfg := &config.ProjectConfig{Fix: []config.FixCommand{{Name: "format", Run: "gofmt -w .", Timeout: 30}}}

	err := writeSettings(dir, cfg, streams, fakeConfirmYes)
	assert.NilError(t, err)

	// settings.json should be updated with merged content.
	data, err := os.ReadFile(filepath.Join(claudeDir, "settings.json"))
	assert.NilError(t, err)

	var merged map[string]interface{}
	assert.NilError(t, json.Unmarshal(data, &merged))

	// Existing env key preserved.
	env := merged["env"].(map[string]interface{})
	assert.Equal(t, env["MY_CUSTOM_VAR"], "hello")

	// Permissions unioned.
	perms := merged["permissions"].(map[string]interface{})
	allow := perms["allow"].([]interface{})
	allowStrs := make([]string, len(allow))
	for i, v := range allow {
		allowStrs[i] = v.(string)
	}
	assert.Assert(t, slices.Contains(allowStrs, "Read"))
	assert.Assert(t, slices.Contains(allowStrs, "Bash(chunk:*)"))

	// Hooks added.
	assert.Assert(t, merged["hooks"] != nil)

	// No example file written.
	_, statErr := os.Stat(filepath.Join(claudeDir, "settings.example.json"))
	assert.Assert(t, os.IsNotExist(statErr))

	assert.Assert(t, bytes.Contains(errOut.Bytes(), []byte("Updated")))
}

func TestWriteSettingsExistingMergeDeclined(t *testing.T) {
	dir := t.TempDir()
	claudeDir := filepath.Join(dir, ".claude")
	assert.NilError(t, os.MkdirAll(claudeDir, 0o755))

	existing := []byte(`{"permissions": {"allow": ["Read"]}}`)
	assert.NilError(t, os.WriteFile(filepath.Join(claudeDir, "settings.json"), existing, 0o644))

	streams, _, _ := testStreams()
	cfg := testCfg(config.ValidateCommand{Name: "test", Run: "go test ./...", Timeout: 60})

	err := writeSettings(dir, cfg, streams, fakeConfirmNo)
	assert.NilError(t, err)

	// Original settings.json untouched.
	data, err := os.ReadFile(filepath.Join(claudeDir, "settings.json"))
	assert.NilError(t, err)
	assert.Equal(t, string(data), string(existing))

	// No example file written on explicit decline.
	_, statErr := os.Stat(filepath.Join(claudeDir, "settings.example.json"))
	assert.Assert(t, errors.Is(statErr, fs.ErrNotExist))
}

func TestWriteSettingsExistingNoTTYFallback(t *testing.T) {
	dir := t.TempDir()
	claudeDir := filepath.Join(dir, ".claude")
	assert.NilError(t, os.MkdirAll(claudeDir, 0o755))

	existing := []byte(`{"permissions": {"allow": ["Read"]}}`)
	assert.NilError(t, os.WriteFile(filepath.Join(claudeDir, "settings.json"), existing, 0o644))

	streams, _, _ := testStreams()
	cfg := testCfg(config.ValidateCommand{Name: "test", Run: "go test ./...", Timeout: 60})

	// Simulates tui.ErrNoTTY — confirm returns an error.
	err := writeSettings(dir, cfg, streams, fakeConfirmErr)
	assert.NilError(t, err)

	// Original untouched, example written.
	data, err := os.ReadFile(filepath.Join(claudeDir, "settings.json"))
	assert.NilError(t, err)
	assert.Equal(t, string(data), string(existing))

	_, statErr := os.Stat(filepath.Join(claudeDir, "settings.example.json"))
	assert.NilError(t, statErr)
}

func TestWriteSettingsAlreadyUpToDate(t *testing.T) {
	dir := t.TempDir()
	claudeDir := filepath.Join(dir, ".claude")
	assert.NilError(t, os.MkdirAll(claudeDir, 0o755))

	cfg := testCfg(config.ValidateCommand{Name: "test", Run: "go test ./...", Timeout: 60})

	// First write — creates settings.json.
	streams1, _, _ := testStreams()
	assert.NilError(t, writeSettings(dir, cfg, streams1, fakeConfirmYes))

	// Second write with same commands — should be up to date.
	streams2, _, errOut := testStreams()
	assert.NilError(t, writeSettings(dir, cfg, streams2, fakeConfirmYes))
	assert.Assert(t, bytes.Contains(errOut.Bytes(), []byte("already up to date")))
}

func TestWriteCodexHooksNewFile(t *testing.T) {
	dir := t.TempDir()
	streams, _, errOut := testStreams()

	cfg := &config.ProjectConfig{Fix: []config.FixCommand{{Name: "format", Run: "gofmt -w .", Timeout: 30}}}

	err := writeCodexHooks(dir, cfg, streams, fakeConfirmYes)
	assert.NilError(t, err)

	data, err := os.ReadFile(filepath.Join(dir, ".codex", "hooks.json"))
	assert.NilError(t, err)

	var parsed map[string]interface{}
	assert.NilError(t, json.Unmarshal(data, &parsed))
	// Must not contain Claude Code-specific keys.
	assert.Assert(t, parsed["$schema"] == nil)
	assert.Assert(t, parsed["permissions"] == nil)
	assert.Assert(t, parsed["hooks"] != nil)
	assert.Assert(t, errOut.Len() > 0)
}

func TestWriteCodexHooksExistingMergeApplied(t *testing.T) {
	dir := t.TempDir()
	codexDir := filepath.Join(dir, ".codex")
	assert.NilError(t, os.MkdirAll(codexDir, 0o755))

	existing := []byte(`{
  "hooks": {
    "Stop": [{"hooks": [{"type": "command", "command": "user-stop-hook", "timeout": 10}]}]
  }
}`)
	assert.NilError(t, os.WriteFile(filepath.Join(codexDir, "hooks.json"), existing, 0o644))

	streams, _, errOut := testStreams()
	cfg := &config.ProjectConfig{Fix: []config.FixCommand{{Name: "format", Run: "gofmt -w .", Timeout: 30}}}

	err := writeCodexHooks(dir, cfg, streams, fakeConfirmYes)
	assert.NilError(t, err)

	data, err := os.ReadFile(filepath.Join(codexDir, "hooks.json"))
	assert.NilError(t, err)

	var merged map[string]interface{}
	assert.NilError(t, json.Unmarshal(data, &merged))

	hooks, ok := merged["hooks"].(map[string]interface{})
	assert.Assert(t, ok, "expected hooks to be a map")
	// Existing user Stop hook preserved.
	assert.Assert(t, hooks["Stop"] != nil)
	// New PostToolUse hook added.
	assert.Assert(t, hooks["PostToolUse"] != nil)

	assert.Assert(t, bytes.Contains(errOut.Bytes(), []byte("Updated")))
}

func TestWriteCodexHooksExistingMergeDeclined(t *testing.T) {
	dir := t.TempDir()
	codexDir := filepath.Join(dir, ".codex")
	assert.NilError(t, os.MkdirAll(codexDir, 0o755))

	existing := []byte(`{"hooks": {}}`)
	assert.NilError(t, os.WriteFile(filepath.Join(codexDir, "hooks.json"), existing, 0o644))

	streams, _, _ := testStreams()
	cfg := testCfg(config.ValidateCommand{Name: "test", Run: "go test ./...", Timeout: 60})

	err := writeCodexHooks(dir, cfg, streams, fakeConfirmNo)
	assert.NilError(t, err)

	// Original hooks.json untouched.
	data, err := os.ReadFile(filepath.Join(codexDir, "hooks.json"))
	assert.NilError(t, err)
	assert.Equal(t, string(data), string(existing))

	// No example file written on explicit decline.
	_, statErr := os.Stat(filepath.Join(codexDir, "hooks.example.json"))
	assert.Assert(t, errors.Is(statErr, fs.ErrNotExist))
}

func TestWriteCodexHooksAlreadyUpToDate(t *testing.T) {
	dir := t.TempDir()
	cfg := testCfg(config.ValidateCommand{Name: "test", Run: "go test ./...", Timeout: 60})

	streams1, _, _ := testStreams()
	assert.NilError(t, writeCodexHooks(dir, cfg, streams1, fakeConfirmYes))

	streams2, _, errOut := testStreams()
	assert.NilError(t, writeCodexHooks(dir, cfg, streams2, fakeConfirmYes))
	assert.Assert(t, bytes.Contains(errOut.Bytes(), []byte("already up to date")))
}

func setupShellEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	t.Setenv(config.EnvShell, "/bin/zsh")
	return home
}

func TestCompletionInstalledFalseWhenNoRCFile(t *testing.T) {
	setupShellEnv(t)

	installed, err := completionInstalled()
	assert.NilError(t, err)
	assert.Assert(t, !installed)
}

func TestCompletionInstalledTrueWhenTagPresent(t *testing.T) {
	home := setupShellEnv(t)
	rcFile := filepath.Join(home, ".zshrc")
	assert.NilError(t, os.WriteFile(rcFile, []byte(completionTag+"\nsource <(chunk completion zsh)\n"), 0o644))

	installed, err := completionInstalled()
	assert.NilError(t, err)
	assert.Assert(t, installed)
}

func TestCompletionInstalledUnsupportedShell(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	t.Setenv(config.EnvShell, "/bin/fish")

	_, err := completionInstalled()
	assert.Assert(t, err != nil)
}

func TestInstallCompletionWritesRCFile(t *testing.T) {
	home := setupShellEnv(t)
	rcFile := filepath.Join(home, ".zshrc")
	streams, _, errOut := testStreams()

	err := installCompletion(streams)
	assert.NilError(t, err)

	data, err := os.ReadFile(rcFile)
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(string(data), completionTag))
	assert.Assert(t, strings.Contains(string(data), "source <(chunk completion zsh)"))
	assert.Assert(t, bytes.Contains(errOut.Bytes(), []byte(ui.Success("Completion installed."))))
}

func TestInstallCompletionSkipsWhenAlreadyInstalled(t *testing.T) {
	home := setupShellEnv(t)
	rcFile := filepath.Join(home, ".zshrc")
	existing := completionTag + "\nsource <(chunk completion zsh)\n"
	assert.NilError(t, os.WriteFile(rcFile, []byte(existing), 0o644))

	streams, _, errOut := testStreams()
	err := installCompletion(streams)
	assert.NilError(t, err)

	// File unchanged.
	data, err := os.ReadFile(rcFile)
	assert.NilError(t, err)
	assert.Equal(t, string(data), existing)
	assert.Assert(t, bytes.Contains(errOut.Bytes(), []byte("already installed")))
}

func initGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitCmds := [][]string{
		{"init"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
	}
	for _, args := range gitCmds {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

func TestWritePrePushHookNewFile(t *testing.T) {
	dir := initGitRepo(t)
	streams, _, errOut := testStreams()

	err := writePrePushHook(dir, streams)
	assert.NilError(t, err)

	data, err := os.ReadFile(filepath.Join(dir, ".git", "hooks", "pre-push"))
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(string(data), "chunk validate"))

	info, err := os.Stat(filepath.Join(dir, ".git", "hooks", "pre-push"))
	assert.NilError(t, err)
	assert.Assert(t, info.Mode()&0o111 != 0, "pre-push hook must be executable")

	assert.Assert(t, bytes.Contains(errOut.Bytes(), []byte("pre-push")))
}

func TestWritePrePushHookAlreadyInstalled(t *testing.T) {
	dir := initGitRepo(t)
	streams, _, _ := testStreams()

	assert.NilError(t, writePrePushHook(dir, streams))

	streams2, _, errOut := testStreams()
	assert.NilError(t, writePrePushHook(dir, streams2))
	assert.Assert(t, bytes.Contains(errOut.Bytes(), []byte("already installed")))
}

// TestInstallSkillsStepUsesProjectScope verifies that installSkillsStep writes
// skills into the project's .claude/skills/ directory, not into the user's
// home directory.
func TestInstallSkillsStepUsesProjectScope(t *testing.T) {
	workDir := t.TempDir()
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)

	streams, _, _ := testStreams()
	installSkillsStep(workDir, streams)

	// Skills must appear in the project dir, not in $HOME.
	skillPath := filepath.Join(workDir, ".claude", "skills", "chunk-sidecar", "SKILL.md")
	_, err := os.Stat(skillPath)
	assert.NilError(t, err, "expected skill installed at %s", skillPath)

	homePath := filepath.Join(home, ".claude", "skills", "chunk-sidecar", "SKILL.md")
	_, err = os.Stat(homePath)
	assert.Assert(t, os.IsNotExist(err), "skill must not be installed under $HOME")
}

func TestPrintInitSummaryWithCommands(t *testing.T) {
	ui.SetColorEnabled(false)
	defer ui.SetColorEnabled(true)

	cfg := &config.ProjectConfig{
		Validate: []config.ValidateCommand{
			{Name: "test", Run: "task test"},
			{Name: "lint", Run: "task lint"},
		},
	}
	streams, _, errOut := testStreams()
	printInitSummary(cfg, streams)

	out := errOut.String()
	assert.Assert(t, strings.Contains(out, "Validate commands:"), "missing section header")
	assert.Assert(t, strings.Contains(out, "test"), "missing test command name")
	assert.Assert(t, strings.Contains(out, "task test"), "missing test command run")
	assert.Assert(t, strings.Contains(out, "lint"), "missing lint command name")
	assert.Assert(t, strings.Contains(out, "task lint"), "missing lint command run")
	assert.Assert(t, strings.Contains(out, ".chunk/config.json"), "missing config path")
	assert.Assert(t, strings.Contains(out, "chunk validate"), "missing validate hint")
	assert.Assert(t, strings.Contains(out, "chunk validate --remote"), "missing remote hint")
	assert.Assert(t, !strings.Contains(out, "CircleCI"), "should not mention CircleCI in init output")
	assert.Assert(t, strings.Contains(out, "chunk validate --remote test"), "missing per-command remote hint")
	assert.Assert(t, strings.Contains(out, "chunk validate --remote lint"), "missing per-command remote hint")
	assert.Assert(t, !strings.Contains(out, "<name>"), "should not show placeholder when commands are known")
}

func TestPrintInitSummaryNoCommands(t *testing.T) {
	ui.SetColorEnabled(false)
	defer ui.SetColorEnabled(true)

	streams, _, errOut := testStreams()
	printInitSummary(&config.ProjectConfig{}, streams)

	out := errOut.String()
	assert.Assert(t, !strings.Contains(out, "Validate commands:"), "should not show command table when none configured")
	assert.Assert(t, strings.Contains(out, ".chunk/config.json"), "missing config path")
	assert.Assert(t, strings.Contains(out, "chunk validate"), "missing validate hint")
}

func TestInstallCompletionBashWritesRCFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	t.Setenv(config.EnvShell, "/bin/bash")

	// Create .bashrc so detectShell prefers it over .bash_profile.
	rcFile := filepath.Join(home, ".bashrc")
	assert.NilError(t, os.WriteFile(rcFile, []byte("# existing config\n"), 0o644))

	streams, _, errOut := testStreams()
	err := installCompletion(streams)
	assert.NilError(t, err)

	data, err := os.ReadFile(rcFile)
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(string(data), completionTag))
	assert.Assert(t, strings.Contains(string(data), "source <(chunk completion bash)"))
	assert.Assert(t, bytes.Contains(errOut.Bytes(), []byte(ui.Success("Completion installed."))))
}

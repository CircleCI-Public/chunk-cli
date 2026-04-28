package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"
)

func setupTempConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(EnvXDGConfigHome, dir)
	t.Setenv(EnvCircleToken, "dummy-circle-token")
	t.Setenv(EnvCircleCIToken, "dummy-circleci-token")
	t.Setenv(EnvAnthropicAPIKey, "dummy-anthropic-key")
	t.Setenv(EnvGitHubToken, "dummy-github-token")
	return dir
}

// --- Dir / Path ---

func TestDir_XDGSet(t *testing.T) {
	t.Setenv(EnvXDGConfigHome, "/tmp/custom-xdg")
	d, err := Dir()
	assert.NilError(t, err)
	assert.Equal(t, d, "/tmp/custom-xdg/chunk")
}

func TestDir_XDGUnset(t *testing.T) {
	t.Setenv(EnvXDGConfigHome, "")
	home, _ := os.UserHomeDir()
	d, err := Dir()
	assert.NilError(t, err)
	assert.Equal(t, d, filepath.Join(home, ".config", "chunk"))
}

func TestPath(t *testing.T) {
	t.Setenv(EnvXDGConfigHome, "/tmp/xdg")
	p, err := Path()
	assert.NilError(t, err)
	assert.Equal(t, p, "/tmp/xdg/chunk/config.json")
}

// --- Load ---

func TestLoad_NoFile(t *testing.T) {
	setupTempConfig(t)

	cfg, err := Load()
	assert.NilError(t, err)
	assert.Equal(t, cfg.AnthropicAPIKey, "")
	assert.Equal(t, cfg.Model, "")
}

func TestLoad_ValidFile(t *testing.T) {
	dir := setupTempConfig(t)
	chunkDir := filepath.Join(dir, "chunk")
	assert.NilError(t, os.MkdirAll(chunkDir, 0o700))

	data := `{"anthropicAPIKey":"sk-test-1234","model":"claude-test"}`
	assert.NilError(t, os.WriteFile(filepath.Join(chunkDir, "config.json"), []byte(data), 0o600))

	cfg, err := Load()
	assert.NilError(t, err)
	assert.Equal(t, cfg.AnthropicAPIKey, "sk-test-1234")
	assert.Equal(t, cfg.Model, "claude-test")
}

func TestLoad_MigratesLegacyAPIKey(t *testing.T) {
	dir := setupTempConfig(t)
	chunkDir := filepath.Join(dir, "chunk")
	assert.NilError(t, os.MkdirAll(chunkDir, 0o700))

	data := `{"apiKey":"sk-legacy-1234","model":"claude-test"}`
	p := filepath.Join(chunkDir, "config.json")
	assert.NilError(t, os.WriteFile(p, []byte(data), 0o600))

	cfg, err := Load()
	assert.NilError(t, err)
	assert.Equal(t, cfg.AnthropicAPIKey, "sk-legacy-1234")
	assert.Equal(t, cfg.LegacyAPIKey, "")
	assert.Equal(t, cfg.Model, "claude-test")

	// Saving the migrated config drops the old "apiKey" field from disk.
	assert.NilError(t, Save(cfg))
	raw, err := os.ReadFile(p)
	assert.NilError(t, err)
	var m map[string]interface{}
	assert.NilError(t, json.Unmarshal(raw, &m))
	_, hasLegacy := m["apiKey"]
	assert.Assert(t, !hasLegacy, "expected legacy apiKey to be dropped, got %v", m)
	assert.Equal(t, m["anthropicAPIKey"], "sk-legacy-1234")
}

func TestLoad_NewKeyTakesPrecedenceOverLegacy(t *testing.T) {
	dir := setupTempConfig(t)
	chunkDir := filepath.Join(dir, "chunk")
	assert.NilError(t, os.MkdirAll(chunkDir, 0o700))

	data := `{"apiKey":"sk-old","anthropicAPIKey":"sk-new"}`
	assert.NilError(t, os.WriteFile(filepath.Join(chunkDir, "config.json"), []byte(data), 0o600))

	cfg, err := Load()
	assert.NilError(t, err)
	assert.Equal(t, cfg.AnthropicAPIKey, "sk-new")
	assert.Equal(t, cfg.LegacyAPIKey, "")
}

func TestLoad_InvalidJSON(t *testing.T) {
	dir := setupTempConfig(t)
	chunkDir := filepath.Join(dir, "chunk")
	assert.NilError(t, os.MkdirAll(chunkDir, 0o700))
	assert.NilError(t, os.WriteFile(filepath.Join(chunkDir, "config.json"), []byte("{bad"), 0o600))

	_, err := Load()
	assert.Assert(t, err != nil, "expected JSON parse error")
}

func TestLoad_Unreadable(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping: root bypasses file permission checks")
	}
	dir := setupTempConfig(t)
	chunkDir := filepath.Join(dir, "chunk")
	assert.NilError(t, os.MkdirAll(chunkDir, 0o700))
	assert.NilError(t, os.WriteFile(filepath.Join(chunkDir, "config.json"), []byte("{}"), 0o000))

	_, err := Load()
	assert.Assert(t, err != nil, "expected permission error")
}

// --- Save ---

func TestSave_CreatesDir(t *testing.T) {
	dir := setupTempConfig(t)

	err := Save(UserConfig{Model: "test-model"})
	assert.NilError(t, err)

	// Verify directory was created with correct permissions
	info, err := os.Stat(filepath.Join(dir, "chunk"))
	assert.NilError(t, err)
	assert.Equal(t, info.Mode().Perm(), os.FileMode(0o700))

	// Verify file permissions
	p, err := Path()
	assert.NilError(t, err)
	finfo, err := os.Stat(p)
	assert.NilError(t, err)
	assert.Equal(t, finfo.Mode().Perm(), os.FileMode(0o600))

	// Verify content
	data, err := os.ReadFile(p)
	assert.NilError(t, err)
	var cfg UserConfig
	assert.NilError(t, json.Unmarshal(data, &cfg))
	assert.Equal(t, cfg.Model, "test-model")
}

func TestSave_OmitsEmptyFields(t *testing.T) {
	setupTempConfig(t)

	err := Save(UserConfig{Model: "m1"})
	assert.NilError(t, err)

	p, err := Path()
	assert.NilError(t, err)
	data, err := os.ReadFile(p)
	assert.NilError(t, err)

	// anthropicAPIKey should be omitted from JSON (omitempty)
	var raw map[string]interface{}
	assert.NilError(t, json.Unmarshal(data, &raw))
	_, hasKey := raw["anthropicAPIKey"]
	assert.Assert(t, !hasKey, "expected anthropicAPIKey to be omitted, got %v", raw)
}

func TestSaveAndLoad_Roundtrip(t *testing.T) {
	setupTempConfig(t)

	original := UserConfig{AnthropicAPIKey: "sk-round", Model: "claude-trip"}
	assert.NilError(t, Save(original))

	loaded, err := Load()
	assert.NilError(t, err)
	assert.Equal(t, loaded.AnthropicAPIKey, original.AnthropicAPIKey)
	assert.Equal(t, loaded.Model, original.Model)
}

// --- Clear ---

func TestClear_AnthropicAPIKey(t *testing.T) {
	setupTempConfig(t)

	assert.NilError(t, Save(UserConfig{AnthropicAPIKey: "sk-secret", Model: "m1"}))

	err := Clear("anthropicAPIKey")
	assert.NilError(t, err)

	cfg, err := Load()
	assert.NilError(t, err)
	assert.Equal(t, cfg.AnthropicAPIKey, "")
	assert.Equal(t, cfg.Model, "m1") // model preserved
}

func TestClear_CircleCIToken(t *testing.T) {
	setupTempConfig(t)

	assert.NilError(t, Save(UserConfig{CircleCIToken: "tok-secret", Model: "m1"}))

	err := Clear("circleCIToken")
	assert.NilError(t, err)

	cfg, err := Load()
	assert.NilError(t, err)
	assert.Equal(t, cfg.CircleCIToken, "")
	assert.Equal(t, cfg.Model, "m1") // model preserved
}

func TestClear_UnknownKey(t *testing.T) {
	setupTempConfig(t)

	err := Clear("badkey")
	assert.Assert(t, err != nil, "expected error for unknown key")
}

func TestClear_NoExistingFile(t *testing.T) {
	setupTempConfig(t)

	// Should succeed even if no config file exists
	err := Clear("anthropicAPIKey")
	assert.NilError(t, err)
}

// --- MaskKey ---

func TestMaskKey(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want string
	}{
		{"empty", "", "****"},
		{"short_3", "abc", "****"},
		{"exact_4", "abcd", "****"},
		{"normal", "sk-ant-api03-AAAA-ZZZZ", "******************ZZZZ"},
		{"five_chars", "12345", "*2345"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, MaskKey(tt.key), tt.want)
		})
	}
}

// --- Resolve ---

func TestResolve_Defaults(t *testing.T) {
	setupTempConfig(t)
	t.Setenv(EnvAnthropicAPIKey, "")

	rc, _ := Resolve("", "")

	assert.Equal(t, rc.AnthropicAPIKey, "")
	assert.Equal(t, rc.Model, DefaultModel)
	assert.Equal(t, rc.ModelSource, "Default")
	assert.Equal(t, rc.AnalyzeModel, AnalyzeModel)
	assert.Equal(t, rc.PromptModel, PromptModel)
}

func TestResolve_EnvKey(t *testing.T) {
	setupTempConfig(t)
	t.Setenv(EnvAnthropicAPIKey, "sk-from-env")

	rc, _ := Resolve("", "")
	assert.Equal(t, rc.AnthropicAPIKey, "sk-from-env")
	assert.Equal(t, rc.AnthropicAPIKeySource, "Environment variable")
}

func TestResolve_EnvOverridesConfigFile(t *testing.T) {
	setupTempConfig(t)
	t.Setenv(EnvAnthropicAPIKey, "sk-from-env")

	assert.NilError(t, Save(UserConfig{AnthropicAPIKey: "sk-from-file"}))

	rc, _ := Resolve("", "")
	assert.Equal(t, rc.AnthropicAPIKey, "sk-from-env")
	assert.Equal(t, rc.AnthropicAPIKeySource, "Environment variable")
}

func TestResolve_FlagOverridesAll(t *testing.T) {
	setupTempConfig(t)
	t.Setenv(EnvAnthropicAPIKey, "sk-from-env")
	assert.NilError(t, Save(UserConfig{AnthropicAPIKey: "sk-from-file", Model: "file-model"}))

	rc, _ := Resolve("sk-from-flag", "flag-model")
	assert.Equal(t, rc.AnthropicAPIKey, "sk-from-flag")
	assert.Equal(t, rc.AnthropicAPIKeySource, "Flag")
	assert.Equal(t, rc.Model, "flag-model")
	assert.Equal(t, rc.ModelSource, "Flag")
}

func TestResolve_ModelFromConfig(t *testing.T) {
	setupTempConfig(t)
	assert.NilError(t, Save(UserConfig{Model: "config-model"}))

	rc, _ := Resolve("", "")
	assert.Equal(t, rc.Model, "config-model")
	assert.Equal(t, rc.ModelSource, SourceConfigFile)
}

// --- ValidConfigKeys ---

func TestValidConfigKeys(t *testing.T) {
	assert.Assert(t, ValidConfigKeys["model"])
	assert.Assert(t, !ValidConfigKeys["anthropicAPIKey"])
	assert.Assert(t, !ValidConfigKeys["badkey"])
}

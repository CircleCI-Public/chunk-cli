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
	t.Setenv("XDG_CONFIG_HOME", dir)
	return dir
}

// --- Dir / Path ---

func TestDir_XDGSet(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/custom-xdg")
	assert.Equal(t, Dir(), "/tmp/custom-xdg/chunk")
}

func TestDir_XDGUnset(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	home, _ := os.UserHomeDir()
	assert.Equal(t, Dir(), filepath.Join(home, ".config", "chunk"))
}

func TestPath(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg")
	assert.Equal(t, Path(), "/tmp/xdg/chunk/config.json")
}

// --- Load ---

func TestLoad_NoFile(t *testing.T) {
	setupTempConfig(t)

	cfg, err := Load()
	assert.NilError(t, err)
	assert.Equal(t, cfg.APIKey, "")
	assert.Equal(t, cfg.Model, "")
}

func TestLoad_ValidFile(t *testing.T) {
	dir := setupTempConfig(t)
	chunkDir := filepath.Join(dir, "chunk")
	assert.NilError(t, os.MkdirAll(chunkDir, 0o700))

	data := `{"apiKey":"sk-test-1234","model":"claude-test"}`
	assert.NilError(t, os.WriteFile(filepath.Join(chunkDir, "config.json"), []byte(data), 0o600))

	cfg, err := Load()
	assert.NilError(t, err)
	assert.Equal(t, cfg.APIKey, "sk-test-1234")
	assert.Equal(t, cfg.Model, "claude-test")
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
	finfo, err := os.Stat(Path())
	assert.NilError(t, err)
	assert.Equal(t, finfo.Mode().Perm(), os.FileMode(0o600))

	// Verify content
	data, err := os.ReadFile(Path())
	assert.NilError(t, err)
	var cfg UserConfig
	assert.NilError(t, json.Unmarshal(data, &cfg))
	assert.Equal(t, cfg.Model, "test-model")
}

func TestSave_OmitsEmptyFields(t *testing.T) {
	setupTempConfig(t)

	err := Save(UserConfig{Model: "m1"})
	assert.NilError(t, err)

	data, err := os.ReadFile(Path())
	assert.NilError(t, err)

	// apiKey should be omitted from JSON (omitempty)
	var raw map[string]interface{}
	assert.NilError(t, json.Unmarshal(data, &raw))
	_, hasKey := raw["apiKey"]
	assert.Assert(t, !hasKey, "expected apiKey to be omitted, got %v", raw)
}

func TestSaveAndLoad_Roundtrip(t *testing.T) {
	setupTempConfig(t)

	original := UserConfig{APIKey: "sk-round", Model: "claude-trip"}
	assert.NilError(t, Save(original))

	loaded, err := Load()
	assert.NilError(t, err)
	assert.Equal(t, loaded.APIKey, original.APIKey)
	assert.Equal(t, loaded.Model, original.Model)
}

// --- ClearAPIKey ---

func TestClearAPIKey(t *testing.T) {
	setupTempConfig(t)

	assert.NilError(t, Save(UserConfig{APIKey: "sk-secret", Model: "m1"}))

	err := ClearAPIKey()
	assert.NilError(t, err)

	cfg, err := Load()
	assert.NilError(t, err)
	assert.Equal(t, cfg.APIKey, "")
	assert.Equal(t, cfg.Model, "m1") // model preserved
}

func TestClearAPIKey_NoExistingFile(t *testing.T) {
	setupTempConfig(t)

	// Should succeed even if no config file exists
	err := ClearAPIKey()
	assert.NilError(t, err)
}

// --- MaskAPIKey ---

func TestMaskAPIKey(t *testing.T) {
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
			assert.Equal(t, MaskAPIKey(tt.key), tt.want)
		})
	}
}

// --- Resolve ---

func TestResolve_Defaults(t *testing.T) {
	setupTempConfig(t)
	t.Setenv("ANTHROPIC_API_KEY", "")

	rc := Resolve("", "")

	assert.Equal(t, rc.APIKey, "")
	assert.Equal(t, rc.Model, DefaultModel)
	assert.Equal(t, rc.ModelSource, "Default")
	assert.Equal(t, rc.AnalyzeModel, AnalyzeModel)
	assert.Equal(t, rc.PromptModel, PromptModel)
}

func TestResolve_EnvKey(t *testing.T) {
	setupTempConfig(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-from-env")

	rc := Resolve("", "")
	assert.Equal(t, rc.APIKey, "sk-from-env")
	assert.Equal(t, rc.APIKeySource, "Environment variable")
}

func TestResolve_EnvOverridesConfigFile(t *testing.T) {
	setupTempConfig(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-from-env")

	assert.NilError(t, Save(UserConfig{APIKey: "sk-from-file"}))

	rc := Resolve("", "")
	assert.Equal(t, rc.APIKey, "sk-from-env")
	assert.Equal(t, rc.APIKeySource, "Environment variable")
}

func TestResolve_FlagOverridesAll(t *testing.T) {
	setupTempConfig(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-from-env")
	assert.NilError(t, Save(UserConfig{APIKey: "sk-from-file", Model: "file-model"}))

	rc := Resolve("sk-from-flag", "flag-model")
	assert.Equal(t, rc.APIKey, "sk-from-flag")
	assert.Equal(t, rc.APIKeySource, "Flag")
	assert.Equal(t, rc.Model, "flag-model")
	assert.Equal(t, rc.ModelSource, "Flag")
}

func TestResolve_ModelFromConfig(t *testing.T) {
	setupTempConfig(t)
	assert.NilError(t, Save(UserConfig{Model: "config-model"}))

	rc := Resolve("", "")
	assert.Equal(t, rc.Model, "config-model")
	assert.Equal(t, rc.ModelSource, "Config file (user config)")
}

// --- ValidConfigKeys ---

func TestValidConfigKeys(t *testing.T) {
	assert.Assert(t, ValidConfigKeys["model"])
	assert.Assert(t, ValidConfigKeys["apiKey"])
	assert.Assert(t, !ValidConfigKeys["badkey"])
}

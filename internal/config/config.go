package config

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"strings"
)

const (
	DefaultModel   = "claude-sonnet-4-5-20250929"
	AnalyzeModel   = "claude-sonnet-4-5-20250929"
	PromptModel    = "claude-opus-4-5-20251101"
	dirPermission  = 0o700
	filePermission = 0o600
)

// UserConfig is the on-disk JSON config.
type UserConfig struct {
	APIKey string `json:"apiKey,omitempty"`
	Model  string `json:"model,omitempty"`
}

// ResolvedConfig holds the final resolved values with their sources.
type ResolvedConfig struct {
	APIKey       string
	APIKeySource string
	Model        string
	ModelSource  string
	AnalyzeModel string
	PromptModel  string
}

// Load reads the config file. Returns empty config if not found.
func Load() (UserConfig, error) {
	data, err := os.ReadFile(Path())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return UserConfig{}, nil
		}
		return UserConfig{}, err
	}
	var cfg UserConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return UserConfig{}, err
	}
	return cfg, nil
}

// Save writes the config file, creating the directory with 0o700 and file with 0o600.
func Save(cfg UserConfig) error {
	dir := Dir()
	if err := os.MkdirAll(dir, dirPermission); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(Path(), data, filePermission)
}

// ClearAPIKey removes the stored API key from config.
func ClearAPIKey() error {
	cfg, err := Load()
	if err != nil {
		return err
	}
	cfg.APIKey = ""
	return Save(cfg)
}

// Resolve computes the final config from flags, env, and file.
// Priority for API key: config file > env > (none).
// Priority for model: flag > config file > default.
func Resolve(flagAPIKey, flagModel string) ResolvedConfig {
	cfg, _ := Load()

	rc := ResolvedConfig{
		AnalyzeModel: AnalyzeModel,
		PromptModel:  PromptModel,
	}

	// API key resolution: flag > config file > env
	switch {
	case flagAPIKey != "":
		rc.APIKey = flagAPIKey
		rc.APIKeySource = "Flag"
	case cfg.APIKey != "":
		rc.APIKey = cfg.APIKey
		rc.APIKeySource = "Config file (user config)"
	case os.Getenv("ANTHROPIC_API_KEY") != "":
		rc.APIKey = os.Getenv("ANTHROPIC_API_KEY")
		rc.APIKeySource = "Environment variable"
	}

	// Model resolution: flag > config file > default
	switch {
	case flagModel != "":
		rc.Model = flagModel
		rc.ModelSource = "Flag"
	case cfg.Model != "":
		rc.Model = cfg.Model
		rc.ModelSource = "Config file (user config)"
	default:
		rc.Model = DefaultModel
		rc.ModelSource = "Default"
	}

	return rc
}

// MaskAPIKey masks all but the last 4 characters with *.
func MaskAPIKey(key string) string {
	if len(key) <= 4 {
		return key
	}
	return strings.Repeat("*", len(key)-4) + key[len(key)-4:]
}

// ValidConfigKeys are the keys accepted by "config set".
var ValidConfigKeys = map[string]bool{
	"model":  true,
	"apiKey": true,
}

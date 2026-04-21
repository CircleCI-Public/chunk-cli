package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

// Model constants define the Claude models used for different operations.
const (
	DefaultModel    = "claude-sonnet-4-6"
	AnalyzeModel    = "claude-sonnet-4-6"
	PromptModel     = "claude-opus-4-6"
	ValidationModel = "claude-haiku-4-5-20251001"
	dirPermission   = 0o700
	filePermission  = 0o600

	// SourceConfigFile is the source label used when a value comes from the user config file.
	SourceConfigFile = "Config file (user config)"
)

// UserConfig is the on-disk JSON config.
type UserConfig struct {
	AnthropicAPIKey string `json:"anthropicAPIKey,omitempty"`
	CircleCIToken   string `json:"circleCIToken,omitempty"`
	GitHubToken     string `json:"gitHubToken,omitempty"`
	Model           string `json:"model,omitempty"`

	// LegacyAPIKey reads the pre-rename "apiKey" field so existing users don't
	// silently lose their stored Anthropic key on upgrade. Migrated into
	// AnthropicAPIKey by Load and dropped on the next Save (omitempty).
	LegacyAPIKey string `json:"apiKey,omitempty"`
}

// ResolvedConfig holds the final resolved values with their sources.
type ResolvedConfig struct {
	AnthropicAPIKey       string
	AnthropicAPIKeySource string
	CircleCIToken         string
	CircleCITokenSource   string
	GitHubToken           string
	GitHubTokenSource     string
	Model                 string
	ModelSource           string
	AnalyzeModel          string
	PromptModel           string
}

// Load reads the config file. Returns empty config if not found.
func Load() (UserConfig, error) {
	p, err := Path()
	if err != nil {
		return UserConfig{}, err
	}
	data, err := os.ReadFile(p)
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
	if cfg.AnthropicAPIKey == "" && cfg.LegacyAPIKey != "" {
		cfg.AnthropicAPIKey = cfg.LegacyAPIKey
	}
	cfg.LegacyAPIKey = ""
	return cfg, nil
}

// Save writes the config file, creating the directory with 0o700 and file with 0o600.
func Save(cfg UserConfig) error {
	dir, err := Dir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, dirPermission); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	p, err := Path()
	if err != nil {
		return err
	}
	return os.WriteFile(p, data, filePermission)
}

// Clear removes a stored config value by key.
func Clear(key string) error {
	cfg, err := Load()
	if err != nil {
		return err
	}
	switch key {
	case "anthropicAPIKey":
		cfg.AnthropicAPIKey = ""
	case "circleCIToken":
		cfg.CircleCIToken = ""
	case "gitHubToken":
		cfg.GitHubToken = ""
	default:
		return fmt.Errorf("unknown config key: %s", key)
	}
	return Save(cfg)
}

// Resolve computes the final config from flags, env, and file.
// Priority for API key: flag > env > config file > (none).
// Priority for model: flag > CODE_REVIEW_CLI_MODEL env > config file > default.
func Resolve(flagAPIKey, flagModel string) (ResolvedConfig, error) {
	cfg, err := Load()

	rc := ResolvedConfig{
		AnalyzeModel: AnalyzeModel,
		PromptModel:  PromptModel,
	}

	// CircleCI token resolution: CIRCLE_TOKEN env > CIRCLECI_TOKEN env > config file
	switch {
	case os.Getenv("CIRCLE_TOKEN") != "":
		rc.CircleCIToken = os.Getenv("CIRCLE_TOKEN")
		rc.CircleCITokenSource = "Environment variable (CIRCLE_TOKEN)"
	case os.Getenv("CIRCLECI_TOKEN") != "":
		rc.CircleCIToken = os.Getenv("CIRCLECI_TOKEN")
		rc.CircleCITokenSource = "Environment variable (CIRCLECI_TOKEN)"
	case cfg.CircleCIToken != "":
		rc.CircleCIToken = cfg.CircleCIToken
		rc.CircleCITokenSource = SourceConfigFile
	}

	// API key resolution: flag > env > config file
	switch {
	case flagAPIKey != "":
		rc.AnthropicAPIKey = flagAPIKey
		rc.AnthropicAPIKeySource = "Flag"
	case os.Getenv("ANTHROPIC_API_KEY") != "":
		rc.AnthropicAPIKey = os.Getenv("ANTHROPIC_API_KEY")
		rc.AnthropicAPIKeySource = "Environment variable"
	case cfg.AnthropicAPIKey != "":
		rc.AnthropicAPIKey = cfg.AnthropicAPIKey
		rc.AnthropicAPIKeySource = SourceConfigFile
	}

	// GitHub token resolution: GITHUB_TOKEN env > config file
	switch {
	case os.Getenv("GITHUB_TOKEN") != "":
		rc.GitHubToken = os.Getenv("GITHUB_TOKEN")
		rc.GitHubTokenSource = "Environment variable (GITHUB_TOKEN)"
	case cfg.GitHubToken != "":
		rc.GitHubToken = cfg.GitHubToken
		rc.GitHubTokenSource = SourceConfigFile
	}

	// Model resolution: flag > env > config file > default
	switch {
	case flagModel != "":
		rc.Model = flagModel
		rc.ModelSource = "Flag"
	case os.Getenv("CODE_REVIEW_CLI_MODEL") != "":
		rc.Model = os.Getenv("CODE_REVIEW_CLI_MODEL")
		rc.ModelSource = "Environment variable"
	case cfg.Model != "":
		rc.Model = cfg.Model
		rc.ModelSource = SourceConfigFile
	default:
		rc.Model = DefaultModel
		rc.ModelSource = "Default"
	}

	return rc, err
}

// MaskKey masks all but the last 4 characters with *.
func MaskKey(key string) string {
	if len(key) <= 4 {
		return "****"
	}
	return strings.Repeat("*", len(key)-4) + key[len(key)-4:]
}

// ValidConfigKeys are the keys accepted by "config set".
// Credentials (anthropicAPIKey, circleCIToken) are intentionally excluded —
// users should use "auth set" which validates before storing.
var ValidConfigKeys = map[string]bool{
	"model": true,
}

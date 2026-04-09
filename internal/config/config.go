package config

import (
	"encoding/json"
	"errors"
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
	APIKey        string `json:"apiKey,omitempty"`
	CircleCIToken string `json:"circleCIToken,omitempty"`
	Model         string `json:"model,omitempty"`
}

// ResolvedConfig holds the final resolved values with their sources.
type ResolvedConfig struct {
	APIKey              string
	APIKeySource        string
	CircleCIToken       string
	CircleCITokenSource string
	Model               string
	ModelSource         string
	AnalyzeModel        string
	PromptModel         string
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

// ClearAPIKey removes the stored API key from config.
func ClearAPIKey() error {
	cfg, err := Load()
	if err != nil {
		return err
	}
	cfg.APIKey = ""
	return Save(cfg)
}

// ClearCircleCIToken removes the stored CircleCI token from config.
func ClearCircleCIToken() error {
	cfg, err := Load()
	if err != nil {
		return err
	}
	cfg.CircleCIToken = ""
	return Save(cfg)
}

// Resolve computes the final config from flags, env, and file.
// Priority for API key: flag > env > config file > (none).
// Priority for model: flag > CODE_REVIEW_CLI_MODEL env > config file > default.
func Resolve(flagAPIKey, flagModel string) ResolvedConfig {
	cfg, _ := Load()

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
		rc.APIKey = flagAPIKey
		rc.APIKeySource = "Flag"
	case os.Getenv("ANTHROPIC_API_KEY") != "":
		rc.APIKey = os.Getenv("ANTHROPIC_API_KEY")
		rc.APIKeySource = "Environment variable"
	case cfg.APIKey != "":
		rc.APIKey = cfg.APIKey
		rc.APIKeySource = SourceConfigFile
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

	return rc
}

// MaskAPIKey masks all but the last 4 characters with *.
func MaskAPIKey(key string) string {
	if len(key) <= 4 {
		return "****"
	}
	return strings.Repeat("*", len(key)-4) + key[len(key)-4:]
}

// ValidConfigKeys are the keys accepted by "config set".
var ValidConfigKeys = map[string]bool{
	"model":  true,
	"apiKey": true,
}

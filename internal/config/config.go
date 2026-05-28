package config

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/sethvargo/go-envconfig"

	"github.com/CircleCI-Public/chunk-cli/internal/keyring"
)

// marshalIndent encodes v as indented JSON without HTML-escaping special characters
// like & < > so that shell commands remain human-readable in config files.
func marshalIndent(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

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

	// SourceProjectConfig is the source label for values from .chunk/config.json.
	SourceProjectConfig = "Project config (.chunk/config.json)"
)

// Chunk-specific environment variable names.
//
//nolint:gosec // env var names, not credentials
const (
	EnvCircleToken        = "CIRCLE_TOKEN"
	EnvCircleCIToken      = "CIRCLECI_TOKEN"
	EnvCircleCIBaseURL    = "CIRCLECI_BASE_URL"
	EnvAnthropicAPIKey    = "ANTHROPIC_API_KEY"
	EnvAnthropicBaseURL   = "ANTHROPIC_BASE_URL"
	EnvGitHubToken        = "GITHUB_TOKEN"
	EnvGitHubAPIURL       = "GITHUB_API_URL"
	EnvModel              = "CODE_REVIEW_CLI_MODEL"
	EnvCircleCIOrgID      = "CIRCLECI_ORG_ID"
	EnvChunkHooksDisabled = "CHUNK_HOOKS_DISABLED"
)

// System/standard environment variable names.
const (
	EnvHome          = "HOME"
	EnvShell         = "SHELL"
	EnvSSHAuthSock   = "SSH_AUTH_SOCK"
	EnvNoColor       = "NO_COLOR"
	EnvXDGConfigHome = "XDG_CONFIG_HOME"
	EnvXDGStateHome  = "XDG_STATE_HOME"
	EnvXDGDataHome   = "XDG_DATA_HOME"
)

// EnvVars holds all environment variables the application reads.
//
//nolint:gosec // env var names, not credentials
type EnvVars struct {
	CircleToken      string `env:"CIRCLE_TOKEN"`
	CircleCIToken    string `env:"CIRCLECI_TOKEN"`
	CircleCIBaseURL  string `env:"CIRCLECI_BASE_URL,default=https://circleci.com"`
	AnthropicAPIKey  string `env:"ANTHROPIC_API_KEY"`
	AnthropicBaseURL string `env:"ANTHROPIC_BASE_URL,default=https://api.anthropic.com"`
	GitHubToken      string `env:"GITHUB_TOKEN"`
	GitHubAPIURL     string `env:"GITHUB_API_URL,default=https://api.github.com"`
	Model            string `env:"CODE_REVIEW_CLI_MODEL"`
	CircleCIOrgID    string `env:"CIRCLECI_ORG_ID"`
	Home             string `env:"HOME"`
	Shell            string `env:"SHELL"`
	SSHAuthSock      string `env:"SSH_AUTH_SOCK"`
	NoColor          string `env:"NO_COLOR"`
	XDGConfigHome    string `env:"XDG_CONFIG_HOME"`
	XDGStateHome     string `env:"XDG_STATE_HOME"`
	XDGDataHome      string `env:"XDG_DATA_HOME"`
}

// LoadEnv populates an EnvVars struct from the process environment.
func LoadEnv(ctx context.Context) (EnvVars, error) {
	var env EnvVars
	if err := envconfig.Process(ctx, &env); err != nil {
		return EnvVars{}, fmt.Errorf("load environment: %w", err)
	}
	return env, nil
}

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
	AnthropicBaseURL      string
	CircleCIToken         string
	CircleCITokenSource   string
	CircleCIBaseURL       string
	GitHubToken           string
	GitHubTokenSource     string
	GitHubAPIURL          string
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
	data, err := marshalIndent(cfg)
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
// Priority for API key: flag > env > keychain (when !insecureStorage) > config file > (none).
// Priority for model: flag > env > config file > default.
// When insecureStorage is true, keychain reads are skipped entirely.
func Resolve(flagAPIKey, flagModel string, insecureStorage bool) (ResolvedConfig, error) {
	cfg, err := Load()

	env, envErr := LoadEnv(context.Background())
	if envErr != nil {
		return ResolvedConfig{}, envErr
	}

	rc := ResolvedConfig{
		AnalyzeModel: AnalyzeModel,
		PromptModel:  PromptModel,
	}

	switch {
	case env.CircleToken != "":
		rc.CircleCIToken = env.CircleToken
		rc.CircleCITokenSource = "Environment variable (" + EnvCircleToken + ")"
	case env.CircleCIToken != "":
		rc.CircleCIToken = env.CircleCIToken
		rc.CircleCITokenSource = "Environment variable (" + EnvCircleCIToken + ")"
	default:
		if !insecureStorage {
			if token, krErr := keyring.Get(keyring.ServiceCircleCI(env.CircleCIBaseURL)); krErr == nil {
				rc.CircleCIToken = token
				rc.CircleCITokenSource = keyring.SourceKeychain
			}
		}
		if rc.CircleCIToken == "" && cfg.CircleCIToken != "" {
			rc.CircleCIToken = cfg.CircleCIToken
			rc.CircleCITokenSource = SourceConfigFile
		}
	}

	switch {
	case flagAPIKey != "":
		rc.AnthropicAPIKey = flagAPIKey
		rc.AnthropicAPIKeySource = "Flag"
	case env.AnthropicAPIKey != "":
		rc.AnthropicAPIKey = env.AnthropicAPIKey
		rc.AnthropicAPIKeySource = "Environment variable"
	default:
		if !insecureStorage {
			if apiKey, krErr := keyring.Get(keyring.ServiceAnthropic(env.AnthropicBaseURL)); krErr == nil {
				rc.AnthropicAPIKey = apiKey
				rc.AnthropicAPIKeySource = keyring.SourceKeychain
			}
		}
		if rc.AnthropicAPIKey == "" && cfg.AnthropicAPIKey != "" {
			rc.AnthropicAPIKey = cfg.AnthropicAPIKey
			rc.AnthropicAPIKeySource = SourceConfigFile
		}
	}

	switch {
	case env.GitHubToken != "":
		rc.GitHubToken = env.GitHubToken
		rc.GitHubTokenSource = "Environment variable (" + EnvGitHubToken + ")"
	default:
		if !insecureStorage {
			if token, krErr := keyring.Get(keyring.ServiceGitHub(env.GitHubAPIURL)); krErr == nil {
				rc.GitHubToken = token
				rc.GitHubTokenSource = keyring.SourceKeychain
			}
		}
		if rc.GitHubToken == "" && cfg.GitHubToken != "" {
			rc.GitHubToken = cfg.GitHubToken
			rc.GitHubTokenSource = SourceConfigFile
		}
	}

	switch {
	case flagModel != "":
		rc.Model = flagModel
		rc.ModelSource = "Flag"
	case env.Model != "":
		rc.Model = env.Model
		rc.ModelSource = "Environment variable"
	case cfg.Model != "":
		rc.Model = cfg.Model
		rc.ModelSource = SourceConfigFile
	default:
		rc.Model = DefaultModel
		rc.ModelSource = "Default"
	}

	rc.CircleCIBaseURL = env.CircleCIBaseURL
	rc.AnthropicBaseURL = env.AnthropicBaseURL
	rc.GitHubAPIURL = env.GitHubAPIURL

	return rc, err
}

// MaskKey masks all but the last 4 characters with *.
func MaskKey(key string) string {
	if len(key) <= 4 {
		return "****"
	}
	return strings.Repeat("*", len(key)-4) + key[len(key)-4:]
}

// ResolveOrgID returns the CircleCI org ID for display in config show.
// Priority: CIRCLECI_ORG_ID env var > orgID in .chunk/config.json for workDir.
func ResolveOrgID(workDir string) (value, source string) {
	env, err := LoadEnv(context.Background())
	if err == nil && env.CircleCIOrgID != "" {
		return env.CircleCIOrgID, "Environment variable (" + EnvCircleCIOrgID + ")"
	}
	projCfg, err := LoadProjectConfig(workDir)
	if err == nil && projCfg.OrgID != "" {
		return projCfg.OrgID, SourceProjectConfig
	}
	return "", ""
}

// ValidConfigKeys are the keys accepted by "config set" that write to the user config.
// Credentials (anthropicAPIKey, circleCIToken) are intentionally excluded —
// users should use "auth set" which validates before storing.
var ValidConfigKeys = map[string]bool{
	"model": true,
}

// ValidProjectConfigKeys are the keys accepted by "config set" that write to
// the project config (.chunk/config.json).
var ValidProjectConfigKeys = map[string]bool{
	"orgID":                   true,
	"validation.sidecarImage": true,
}

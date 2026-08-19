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

	"github.com/google/uuid"
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
	EnvChunkNoTelemetry   = "CHUNK_NO_TELEMETRY"
	EnvChunkSessionID     = "CHUNK_SESSION_ID"
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
	EnvNoAnalytics   = "NO_ANALYTICS"
	EnvDoNotTrack    = "DO_NOT_TRACK"
	EnvCI            = "CI"
)

// noTelemetryEnvVars are well-known environment variables that disable
// telemetry regardless of the persisted user preference.
var noTelemetryEnvVars = []string{EnvChunkNoTelemetry, EnvNoAnalytics, EnvDoNotTrack, EnvCI}

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
	AnthropicAPIKey    string `json:"anthropicAPIKey,omitempty"`
	CircleCIToken      string `json:"circleCIToken,omitempty"`
	GitHubToken        string `json:"gitHubToken,omitempty"`
	Model              string `json:"model,omitempty"`
	UseSSHIdentityFile bool   `json:"useSSHIdentityFile,omitempty"`
	InstanceID         string `json:"instanceID,omitempty"`

	// Telemetry is the persisted telemetry preference: true enables it,
	// false disables it. nil means no preference has been set, in which
	// case telemetry defaults to enabled (it is opt-out).
	Telemetry *bool `json:"telemetry,omitempty"`

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
	UseSSHIdentityFile    bool
}

func resolveCircleCIToken(env EnvVars, cfg UserConfig) (string, string) {
	switch {
	case env.CircleToken != "":
		return env.CircleToken, "Environment variable (" + EnvCircleToken + ")"
	case env.CircleCIToken != "":
		return env.CircleCIToken, "Environment variable (" + EnvCircleCIToken + ")"
	case cfg.CircleCIToken != "":
		return cfg.CircleCIToken, SourceConfigFile
	default:
		if token, err := keyring.Get(keyring.ServiceCircleCI(env.CircleCIBaseURL)); err == nil {
			return token, keyring.SourceKeychain
		}
	}
	return "", ""
}

func resolveAnthropicAPIKey(flagAPIKey string, env EnvVars, cfg UserConfig) (string, string) {
	switch {
	case flagAPIKey != "":
		return flagAPIKey, "Flag"
	case env.AnthropicAPIKey != "":
		return env.AnthropicAPIKey, "Environment variable"
	case cfg.AnthropicAPIKey != "":
		return cfg.AnthropicAPIKey, SourceConfigFile
	default:
		if apiKey, err := keyring.Get(keyring.ServiceAnthropic(env.AnthropicBaseURL)); err == nil {
			return apiKey, keyring.SourceKeychain
		}
	}
	return "", ""
}

func resolveGitHubToken(env EnvVars, cfg UserConfig) (string, string) {
	switch {
	case env.GitHubToken != "":
		return env.GitHubToken, "Environment variable (" + EnvGitHubToken + ")"
	case cfg.GitHubToken != "":
		return cfg.GitHubToken, SourceConfigFile
	default:
		if token, err := keyring.Get(keyring.ServiceGitHub(env.GitHubAPIURL)); err == nil {
			return token, keyring.SourceKeychain
		}
	}
	return "", ""
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

// IsTelemetry reports whether telemetry should be collected, honoring (in
// order) well-known opt-out environment variables and the persisted
// telemetry preference. Telemetry is opt-out: it defaults to enabled when no
// preference has been set.
func IsTelemetry(cfg UserConfig) bool {
	for _, env := range noTelemetryEnvVars {
		if os.Getenv(env) != "" {
			return false
		}
	}
	if cfg.Telemetry == nil {
		return true
	}
	return *cfg.Telemetry
}

// EnsureInstanceID returns the persisted anonymous instance ID used to
// associate telemetry events with a single install, generating and saving
// one on first run.
func EnsureInstanceID() (uuid.UUID, error) {
	cfg, err := Load()
	if err != nil {
		return uuid.Nil, err
	}
	if cfg.InstanceID != "" {
		id, err := uuid.Parse(cfg.InstanceID)
		if err == nil {
			return id, nil
		}
	}
	id := uuid.New()
	cfg.InstanceID = id.String()
	if err := Save(cfg); err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

// Resolve computes the final config from flags, env, file, and keychain.
// Priority for API key: flag > env > config file > keychain > (none).
// Priority for model: flag > env > config file > default.
// insecureStorage affects credential writes elsewhere, but reads always use the
// same precedence order.
func Resolve(flagAPIKey, flagModel string, _ bool) (ResolvedConfig, error) {
	cfg, err := Load()

	env, envErr := LoadEnv(context.Background())
	if envErr != nil {
		return ResolvedConfig{}, envErr
	}

	rc := ResolvedConfig{
		AnalyzeModel: AnalyzeModel,
		PromptModel:  PromptModel,
	}
	rc.CircleCIToken, rc.CircleCITokenSource = resolveCircleCIToken(env, cfg)
	rc.AnthropicAPIKey, rc.AnthropicAPIKeySource = resolveAnthropicAPIKey(flagAPIKey, env, cfg)
	rc.GitHubToken, rc.GitHubTokenSource = resolveGitHubToken(env, cfg)

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
	rc.UseSSHIdentityFile = cfg.UseSSHIdentityFile

	return rc, err
}

// ResolveCircleCI returns only the CircleCI-related config needed by sidecar
// commands. It intentionally skips Anthropic and GitHub resolution so callers
// that only need CircleCI auth avoid unrelated keyring work.
func ResolveCircleCI(_ bool) (ResolvedConfig, error) {
	cfg, err := Load()
	if err != nil {
		return ResolvedConfig{}, err
	}

	env, envErr := LoadEnv(context.Background())
	if envErr != nil {
		return ResolvedConfig{}, envErr
	}

	rc := ResolvedConfig{
		AnalyzeModel:       AnalyzeModel,
		PromptModel:        PromptModel,
		CircleCIBaseURL:    env.CircleCIBaseURL,
		AnthropicBaseURL:   env.AnthropicBaseURL,
		GitHubAPIURL:       env.GitHubAPIURL,
		UseSSHIdentityFile: cfg.UseSSHIdentityFile,
	}
	rc.CircleCIToken, rc.CircleCITokenSource = resolveCircleCIToken(env, cfg)
	return rc, nil
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
	"model":              true,
	"useSSHIdentityFile": true,
	"telemetry":          true,
}

// ValidProjectConfigKeys are the keys accepted by "config set" that write to
// the project config (.chunk/config.json).
var ValidProjectConfigKeys = map[string]bool{
	"orgID":                   true,
	"validation.sidecarImage": true,
}

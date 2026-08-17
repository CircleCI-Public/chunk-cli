package config

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	json "github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
	"github.com/google/uuid"
	"github.com/sethvargo/go-envconfig"

	"github.com/CircleCI-Public/chunk-cli/internal/keyring"
)

// writeFileAtomic writes data to path through a temporary file in the same
// directory followed by a rename, which is atomic within a filesystem. A config
// write that is interrupted — a crash, a full disk, a killed process — therefore
// leaves the previous file intact rather than a truncated one. That matters more
// than usual here: every write path refuses a config it cannot parse, so a
// half-written file would lock the user out until they repaired it by hand.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmp := f.Name()
	// Any failure past this point leaves the temp file behind, and the real file
	// untouched, which is the outcome worth having.
	written, err := writeAndClose(f, data, perm)
	if !written {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename %s to %s: %w", tmp, path, err)
	}
	return nil
}

// writeAndClose writes data to f, applies perm, and closes it. The bool reports
// whether f is fully on disk and ready to be renamed into place. Close is not
// deferred: the file has to be closed before the rename, and its error is part
// of whether the write succeeded.
func writeAndClose(f *os.File, data []byte, perm os.FileMode) (bool, error) {
	name := f.Name()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return false, fmt.Errorf("write %s: %w", name, err)
	}
	// Durability before the rename, so a crash cannot leave the renamed file
	// pointing at unflushed contents.
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return false, fmt.Errorf("sync %s: %w", name, err)
	}
	// CreateTemp makes the file 0o600; widen it only where the caller asks.
	if err := f.Chmod(perm); err != nil {
		_ = f.Close()
		return false, fmt.Errorf("chmod %s: %w", name, err)
	}
	if err := f.Close(); err != nil {
		return false, fmt.Errorf("close %s: %w", name, err)
	}
	return true, nil
}

// marshalIndent encodes v as indented JSON, the format chunk uses for its config
// files. encoding/json/v2 does not HTML-escape by default, so shell operators
// like && in commands stay human-readable.
//
// Keys the config types do not model travel in their jsontext.Value fields
// tagged `json:",embed"` and are written back out here, which is what keeps a
// write from deleting hand-added keys. That only works under encoding/json/v2:
// v1 ignores the unknown tag option and would emit a literal "Extra" member.
func marshalIndent(v any) ([]byte, error) {
	return json.Marshal(v, jsontext.WithIndent("  "))
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
	UseSSHIdentityFile bool   `json:"useSSHIdentityFile,omitzero"`
	InstanceID         string `json:"instanceID,omitempty"`

	// Telemetry is the persisted telemetry preference: true enables it,
	// false disables it. nil means no preference has been set, in which
	// case telemetry defaults to enabled (it is opt-out).
	Telemetry *bool `json:"telemetry,omitempty"`

	// LegacyAPIKey reads the pre-rename "apiKey" field so existing users don't
	// silently lose their stored Anthropic key on upgrade. Migrated into
	// AnthropicAPIKey by Load and dropped on the next Save (omitempty).
	LegacyAPIKey string `json:"apiKey,omitempty"`

	// Extra holds object members this type does not model, so a write does not
	// delete keys hand-added to the config file. See marshalIndent.
	Extra jsontext.Value `json:",embed"`
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

// ErrParseUserConfig marks a user config file that exists but is not valid JSON.
// Both Load and Save report it: the keys chunk does not model cannot be read out
// of such a file, so a write would drop them.
var ErrParseUserConfig = errors.New("parse config file")

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
		return UserConfig{}, fmt.Errorf("%w: %w", ErrParseUserConfig, err)
	}
	if cfg.AnthropicAPIKey == "" && cfg.LegacyAPIKey != "" {
		cfg.AnthropicAPIKey = cfg.LegacyAPIKey
	}
	cfg.LegacyAPIKey = ""
	return cfg, nil
}

// Save writes the config file, creating the directory with 0o700 and file with
// 0o600. Keys the UserConfig type does not model ride along in cfg.Extra and are
// written back out, so callers must load before saving: a UserConfig built from
// scratch has no unknown keys to carry and will drop the ones on disk.
//
// An existing config that does not parse is refused with ErrParseUserConfig and
// the file is left alone. Load rejects the same file, so a caller that loads
// first never gets here — the check is what stops a writer that forgot to, since
// cfg would then carry no unknown keys and this would flatten the file.
func Save(cfg UserConfig) error {
	dir, err := Dir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, dirPermission); err != nil {
		return err
	}
	p, err := Path()
	if err != nil {
		return err
	}
	if err := checkUserConfigParses(p); err != nil {
		return err
	}
	data, err := marshalIndent(cfg)
	if err != nil {
		return err
	}
	return writeFileAtomic(p, data, filePermission)
}

// checkUserConfigParses reports an error unless the file at p is safe to replace:
// either it does not exist, or it parses and so was loadable into the cfg being
// written. A file that cannot be read is not safe — it may be full of unknown
// keys that simply cannot be seen from here — so it is refused rather than
// flattened.
func checkUserConfigParses(p string) error {
	data, err := os.ReadFile(p)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return fmt.Errorf("read %s: %w", p, err)
	}
	var probe UserConfig
	if err := json.Unmarshal(data, &probe); err != nil {
		// Name the file: this is the only way out, and unlike the project config
		// there is no `--force` to overwrite it, so the user has to find and fix
		// it by hand.
		return fmt.Errorf("%w: %s: %w", ErrParseUserConfig, p, err)
	}
	return nil
}

// UnknownKeys returns the names in the loaded config that chunk does not model.
// They are preserved on save; reporting them lets a caller point out a typo
// instead of keeping it forever.
func (c UserConfig) UnknownKeys() []string {
	names := appendUnknownKeys(nil, "", c.Extra)
	slices.Sort(names)
	return names
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

package env

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestEnv builds an isolated environment for CLI invocations.
type TestEnv struct {
	HomeDir      string
	GithubURL    string
	AnthropicURL string
	CircleCIURL  string
	GithubToken  string
	AnthropicKey string
	CircleToken  string
	Extra        map[string]string
}

// NewTestEnv creates a TestEnv with a fresh temp HOME directory.
func NewTestEnv(t *testing.T) *TestEnv {
	t.Helper()
	home := t.TempDir()
	return &TestEnv{
		HomeDir:      home,
		GithubToken:  "fake-github-token",
		AnthropicKey: "sk-ant-fake-key",
		CircleToken:  "fake-circle-token",
		Extra:        map[string]string{},
	}
}

// Environ returns the environment as a []string for exec.Cmd.Env.
func (e *TestEnv) Environ() []string {
	configDir := filepath.Join(e.HomeDir, ".config")

	env := []string{
		fmt.Sprintf("HOME=%s", e.HomeDir),
		fmt.Sprintf("XDG_CONFIG_HOME=%s", configDir),
		fmt.Sprintf("PATH=%s", os.Getenv("PATH")),
		"NO_COLOR=1",
		"TERM=dumb",
	}

	if e.GithubToken != "" {
		env = append(env, fmt.Sprintf("GITHUB_TOKEN=%s", e.GithubToken))
	}
	if e.AnthropicKey != "" {
		env = append(env, fmt.Sprintf("ANTHROPIC_API_KEY=%s", e.AnthropicKey))
	}
	if e.CircleToken != "" {
		env = append(env, fmt.Sprintf("CIRCLE_TOKEN=%s", e.CircleToken))
	}
	if e.GithubURL != "" {
		env = append(env, fmt.Sprintf("GITHUB_API_URL=%s", e.GithubURL))
	}
	if e.AnthropicURL != "" {
		env = append(env, fmt.Sprintf("ANTHROPIC_BASE_URL=%s", e.AnthropicURL))
	}
	if e.CircleCIURL != "" {
		env = append(env, fmt.Sprintf("CIRCLECI_BASE_URL=%s", e.CircleCIURL))
	}

	for k, v := range e.Extra {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}

	return env
}

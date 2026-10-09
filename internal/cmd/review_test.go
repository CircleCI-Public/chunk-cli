package cmd

import (
	"errors"
	"strings"
	"testing"

	gokeyring "github.com/zalando/go-keyring"
	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/harness/claudecode"
	"github.com/CircleCI-Public/chunk-cli/internal/keyring"
)

const reviewBaseURL = "https://api.anthropic.com"

func storeBothClaudeCredentials(t *testing.T) {
	t.Helper()
	gokeyring.MockInit()
	assert.NilError(t, keyring.Set(keyring.ServiceAnthropic(reviewBaseURL), "sk-ant-api"))
	assert.NilError(t, keyring.Set(keyring.ServiceAnthropicOAuth(reviewBaseURL), "sk-ant-oat01-tok"))
}

func TestCredentialRejectedRemovesOnlyTheRejectedKey(t *testing.T) {
	storeBothClaudeCredentials(t)

	err := credentialRejected(claudecode.Credential{EnvVar: config.EnvAnthropicAPIKey}, keyring.SourceKeychain, reviewBaseURL, claudecode.ErrCredentialRejected)
	var ue *userError
	assert.Assert(t, errors.As(err, &ue))
	assert.Equal(t, ue.msg, "Anthropic rejected the stored credential, so it has been removed.")

	_, getErr := keyring.Get(keyring.ServiceAnthropic(reviewBaseURL))
	assert.Assert(t, getErr != nil, "rejected API key should be removed")
	token, getErr := keyring.Get(keyring.ServiceAnthropicOAuth(reviewBaseURL))
	assert.NilError(t, getErr)
	assert.Equal(t, token, "sk-ant-oat01-tok")
}

func TestCredentialRejectedRemovesOnlyTheRejectedToken(t *testing.T) {
	storeBothClaudeCredentials(t)

	err := credentialRejected(claudecode.Credential{EnvVar: config.EnvClaudeOAuthToken}, keyring.SourceKeychain, reviewBaseURL, claudecode.ErrCredentialRejected)
	var ue *userError
	assert.Assert(t, errors.As(err, &ue))
	assert.Assert(t, strings.Contains(ue.suggestion, "anthropic-oauth"), "suggestion: %s", ue.suggestion)

	_, getErr := keyring.Get(keyring.ServiceAnthropicOAuth(reviewBaseURL))
	assert.Assert(t, getErr != nil, "rejected token should be removed")
	key, getErr := keyring.Get(keyring.ServiceAnthropic(reviewBaseURL))
	assert.NilError(t, getErr)
	assert.Equal(t, key, "sk-ant-api")
}

func TestCredentialRejectedFromEnvironmentKeepsTheKeychain(t *testing.T) {
	storeBothClaudeCredentials(t)

	err := credentialRejected(claudecode.Credential{EnvVar: config.EnvAnthropicAPIKey}, "Environment variable", reviewBaseURL, claudecode.ErrCredentialRejected)
	var ue *userError
	assert.Assert(t, errors.As(err, &ue))
	assert.Equal(t, ue.msg, "Anthropic rejected the credential in "+config.EnvAnthropicAPIKey+".")

	_, getErr := keyring.Get(keyring.ServiceAnthropic(reviewBaseURL))
	assert.NilError(t, getErr)
}

// A key stored with --insecure-storage is not in the environment, so telling
// the user to unset an env var would leave them stuck.
func TestCredentialRejectedFromConfigFileNamesTheFile(t *testing.T) {
	err := credentialRejected(claudecode.Credential{EnvVar: config.EnvAnthropicAPIKey}, "Config file (/home/u/.config/chunk/config.json)", reviewBaseURL, claudecode.ErrCredentialRejected)
	var ue *userError
	assert.Assert(t, errors.As(err, &ue))
	assert.Equal(t, ue.msg, "Anthropic rejected the API key in config file (/home/u/.config/chunk/config.json).")
	assert.Assert(t, strings.Contains(ue.suggestion, "--insecure-storage"), "suggestion: %s", ue.suggestion)
	assert.Equal(t, ue.exitCode, ExitAuthError)
}

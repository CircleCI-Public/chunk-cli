package cmd

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	gokeyring "github.com/zalando/go-keyring"
	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/keyring"
	"github.com/CircleCI-Public/chunk-cli/internal/review"
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

	err := credentialRejected(review.Credential{EnvVar: config.EnvAnthropicAPIKey}, keyring.SourceKeychain, reviewBaseURL, review.ErrCredentialRejected)
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

	err := credentialRejected(review.Credential{EnvVar: config.EnvClaudeOAuthToken}, keyring.SourceKeychain, reviewBaseURL, review.ErrCredentialRejected)
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

	err := credentialRejected(review.Credential{EnvVar: config.EnvAnthropicAPIKey}, "Environment variable", reviewBaseURL, review.ErrCredentialRejected)
	var ue *userError
	assert.Assert(t, errors.As(err, &ue))
	assert.Equal(t, ue.msg, "Anthropic rejected the credential in "+config.EnvAnthropicAPIKey+".")

	_, getErr := keyring.Get(keyring.ServiceAnthropic(reviewBaseURL))
	assert.NilError(t, getErr)
}

// A key stored with --insecure-storage is not in the environment, so telling
// the user to unset an env var would leave them stuck.
func TestCredentialRejectedFromConfigFileNamesTheFile(t *testing.T) {
	err := credentialRejected(review.Credential{EnvVar: config.EnvAnthropicAPIKey}, "Config file (/home/u/.config/chunk/config.json)", reviewBaseURL, review.ErrCredentialRejected)
	var ue *userError
	assert.Assert(t, errors.As(err, &ue))
	assert.Equal(t, ue.msg, "Anthropic rejected the API key in config file (/home/u/.config/chunk/config.json).")
	assert.Assert(t, strings.Contains(ue.suggestion, "--insecure-storage"), "suggestion: %s", ue.suggestion)
	assert.Equal(t, ue.exitCode, ExitAuthError)
}

// A failed review's raw output is claude's JSON result, which the error
// already summarises and --json still carries.
func TestPrintReviewsSkipsRawJSONOutputOnFailure(t *testing.T) {
	var out bytes.Buffer
	printReviews(iostream.Streams{Out: &out, Err: &bytes.Buffer{}}, []review.Result{
		{Prompt: "broken", SidecarID: "sb-2", Error: "claude exited 1: overloaded", Output: `{"type":"result","is_error":true}`},
	})
	assert.Equal(t, out.String(), "## broken\n\n_Review failed on sb-2: claude exited 1: overloaded_\n\n")
}

func TestPrintReviews(t *testing.T) {
	var out bytes.Buffer
	printReviews(iostream.Streams{Out: &out, Err: &bytes.Buffer{}}, []review.Result{
		{
			Prompt:  "correctness",
			Summary: "Two problems.",
			Findings: []review.Finding{
				{File: "main.go", Line: 12, Severity: review.SeverityHigh, Confidence: 90, Claim: "nil map write", FailureScenario: "empty config panics"},
				{File: "go.mod", Severity: review.SeverityLow, Confidence: 40, Claim: "stale toolchain", FailureScenario: "builds use an old Go"},
			},
		},
		{Prompt: "clean", Summary: "Nothing found.", Findings: []review.Finding{}},
		{Prompt: "broken", SidecarID: "sb-2", Error: "claude exited 1", Output: "partial"},
	})

	want := "## correctness\n\n" +
		"Two problems.\n" +
		"\n- **high** `main.go:12` nil map write (confidence 90%)\n  empty config panics\n" +
		"\n- **low** `go.mod` stale toolchain (confidence 40%)\n  builds use an old Go\n" +
		"\n## clean\n\n" +
		"Nothing found.\n" +
		"\n## broken\n\n" +
		"_Review failed on sb-2: claude exited 1_\n\n" +
		"partial\n"
	assert.Equal(t, out.String(), want)
}

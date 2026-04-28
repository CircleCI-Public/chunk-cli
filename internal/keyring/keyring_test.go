package keyring_test

import (
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/keyring"
)

func TestCircleCITokenKey(t *testing.T) {
	tests := []struct {
		baseURL string
		want    string
	}{
		{"", "com.circleci.cli:https://circleci.com"},
		{"https://circleci.com", "com.circleci.cli:https://circleci.com"},
		{"https://circleci.mycompany.com", "com.circleci.cli:https://circleci.mycompany.com"},
	}
	for _, tt := range tests {
		got := keyring.CircleCITokenKey(tt.baseURL)
		assert.Equal(t, got, tt.want)
	}
}

func TestStaticKeys(t *testing.T) {
	assert.Equal(t, keyring.KeyAnthropicAPIKey, "anthropic-api-key")
	assert.Equal(t, keyring.KeyGitHubToken, "github-token")
}

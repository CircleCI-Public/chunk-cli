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
		assert.Equal(t, keyring.CircleCITokenKey(tt.baseURL), tt.want)
	}
}

func TestGitHubTokenKey(t *testing.T) {
	tests := []struct {
		apiURL string
		want   string
	}{
		{"", "com.circleci.cli:https://api.github.com"},
		{"https://api.github.com", "com.circleci.cli:https://api.github.com"},
		{"https://github.mycompany.com/api/v3", "com.circleci.cli:https://github.mycompany.com/api/v3"},
	}
	for _, tt := range tests {
		assert.Equal(t, keyring.GitHubTokenKey(tt.apiURL), tt.want)
	}
}

func TestAnthropicKeyKey(t *testing.T) {
	tests := []struct {
		baseURL string
		want    string
	}{
		{"", "com.circleci.cli:https://api.anthropic.com"},
		{"https://api.anthropic.com", "com.circleci.cli:https://api.anthropic.com"},
		{"https://anthropic-proxy.mycompany.com", "com.circleci.cli:https://anthropic-proxy.mycompany.com"},
	}
	for _, tt := range tests {
		assert.Equal(t, keyring.AnthropicKeyKey(tt.baseURL), tt.want)
	}
}

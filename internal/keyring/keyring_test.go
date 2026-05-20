package keyring_test

import (
	"os"
	"testing"

	gokeyring "github.com/zalando/go-keyring"
	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/keyring"
)

func TestMain(m *testing.M) {
	gokeyring.MockInit()
	os.Exit(m.Run())
}

func TestGetReturnsNotFoundWhenEmpty(t *testing.T) {
	_, err := keyring.Get(keyring.ServiceAnthropic("https://api.anthropic.com"))
	assert.ErrorIs(t, err, keyring.ErrNotFound)
}

func TestSetAndGetRoundTrip(t *testing.T) {
	assert.NilError(t, keyring.Set(keyring.ServiceCircleCI("https://circleci.com"), "token123"))
	val, err := keyring.Get(keyring.ServiceCircleCI("https://circleci.com"))
	assert.NilError(t, err)
	assert.Equal(t, val, "token123")
}

func TestDeleteNonExistentSucceeds(t *testing.T) {
	assert.NilError(t, keyring.Delete(keyring.ServiceGitHub("https://api.github.com")))
}

func TestDeleteRemovesStoredCredential(t *testing.T) {
	assert.NilError(t, keyring.Set(keyring.ServiceGitHub("https://api.github.com"), "gh-token"))
	assert.NilError(t, keyring.Delete(keyring.ServiceGitHub("https://api.github.com")))
	_, err := keyring.Get(keyring.ServiceGitHub("https://api.github.com"))
	assert.ErrorIs(t, err, keyring.ErrNotFound)
}

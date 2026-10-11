package keyring_test

import (
	"os"
	"strings"
	"testing"

	gokeyring "github.com/zalando/go-keyring"
	"gotest.tools/v3/assert"

	"github.com/smeltdev/smelt/internal/brand"
	"github.com/smeltdev/smelt/internal/keyring"
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

// smelt coexists with CircleCI's own CLI, which stores its credentials under
// its own service names. A credential there is not smelt's, and smelt's names
// must not be where CircleCI's tool looks.
func TestServicesAreNotInCircleCIsNamespace(t *testing.T) {
	const theirs = "com.circleci.chunk:"
	for _, svc := range []string{
		keyring.ServiceCircleCI("https://circleci.com"),
		keyring.ServiceAnthropic("https://api.anthropic.com"),
		keyring.ServiceAnthropicOAuth("https://api.anthropic.com"),
		keyring.ServiceGitHub("https://api.github.com"),
	} {
		assert.Assert(t, !strings.HasPrefix(svc, theirs), svc)
		assert.Assert(t, strings.HasPrefix(svc, brand.KeychainPrefix), svc)
	}
}

func TestIgnoresCredentialsStoredByCircleCIsCLI(t *testing.T) {
	assert.NilError(t, gokeyring.Set("com.circleci.chunk:anthropic:https://api.anthropic.com", "someone-else", "their-key"))
	_, err := keyring.Get(keyring.ServiceAnthropic("https://api.anthropic.com"))
	assert.ErrorIs(t, err, keyring.ErrNotFound)
}

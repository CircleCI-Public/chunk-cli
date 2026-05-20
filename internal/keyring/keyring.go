package keyring

import (
	"errors"
	"os/user"
	"strings"
	"time"

	"github.com/zalando/go-keyring"
)

const (
	servicePrefix = "com.circleci.chunk:"

	// SourceKeychain is the source label used when a value comes from the system keychain.
	SourceKeychain = "System keychain"

	opTimeout = 3 * time.Second
)

// ServiceCircleCI returns the keychain service key for the given CircleCI base URL.
func ServiceCircleCI(baseURL string) string {
	return servicePrefix + "circleci:" + strings.TrimRight(baseURL, "/")
}

// ServiceAnthropic returns the keychain service key for the given Anthropic base URL.
func ServiceAnthropic(baseURL string) string {
	return servicePrefix + "anthropic:" + strings.TrimRight(baseURL, "/")
}

// ServiceGitHub returns the keychain service key for the given GitHub API URL.
func ServiceGitHub(baseURL string) string {
	return servicePrefix + "github:" + strings.TrimRight(baseURL, "/")
}

// ErrNotFound is returned by Get when no credential is stored or the keyring is unavailable.
var ErrNotFound = errors.New("credential not found in keychain")

func username() string {
	u, err := user.Current()
	if err != nil {
		return "chunk"
	}
	return u.Username
}

// Get retrieves a credential. Returns ErrNotFound if absent or keyring unavailable.
func Get(service string) (string, error) {
	type result struct {
		val string
		err error
	}
	ch := make(chan result, 1)
	go func() {
		val, err := keyring.Get(service, username())
		ch <- result{val, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			return "", ErrNotFound
		}
		return r.val, nil
	case <-time.After(opTimeout):
		return "", ErrNotFound
	}
}

// Set stores a credential in the system keychain.
func Set(service, secret string) error {
	ch := make(chan error, 1)
	go func() {
		ch <- keyring.Set(service, username(), secret)
	}()
	select {
	case err := <-ch:
		return err
	case <-time.After(opTimeout):
		return errors.New("keyring Set timed out")
	}
}

// Delete removes a credential. Returns nil if not present.
func Delete(service string) error {
	ch := make(chan error, 1)
	go func() {
		err := keyring.Delete(service, username())
		if errors.Is(err, keyring.ErrNotFound) {
			err = nil
		}
		ch <- err
	}()
	select {
	case err := <-ch:
		return err
	case <-time.After(opTimeout):
		return errors.New("keyring Delete timed out")
	}
}

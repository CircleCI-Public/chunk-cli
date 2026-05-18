package keyring

import (
	"context"
	"errors"
	"fmt"
	"time"

	zkeyring "github.com/zalando/go-keyring"
)

const (
	service = "com.circleci.cli"
	timeout = 3 * time.Second
)

// CircleCITokenKey returns the keychain account name for a CircleCI token
// scoped to the given base URL.
func CircleCITokenKey(baseURL string) string {
	if baseURL == "" {
		baseURL = "https://circleci.com"
	}
	return service + ":" + baseURL
}

// GitHubTokenKey returns the keychain account name for a GitHub token scoped
// to the given API URL.
func GitHubTokenKey(apiURL string) string {
	if apiURL == "" {
		apiURL = "https://api.github.com"
	}
	return service + ":" + apiURL
}

// AnthropicKeyKey returns the keychain account name for an Anthropic API key
// scoped to the given base URL.
func AnthropicKeyKey(baseURL string) string {
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}
	return service + ":" + baseURL
}

// Get retrieves a credential from the system keychain.
// Returns ("", nil) if the key is not found.
func Get(key string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	type result struct {
		val string
		err error
	}
	done := make(chan result, 1)
	go func() {
		val, err := zkeyring.Get(service, key)
		done <- result{val, err}
	}()

	select {
	case <-ctx.Done():
		return "", fmt.Errorf("keychain timeout")
	case r := <-done:
		if errors.Is(r.err, zkeyring.ErrNotFound) {
			return "", nil
		}
		return r.val, r.err
	}
}

// Set stores a credential in the system keychain.
func Set(key, value string) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- zkeyring.Set(service, key, value)
	}()

	select {
	case <-ctx.Done():
		return fmt.Errorf("keychain timeout")
	case err := <-done:
		return err
	}
}

// Delete removes a credential from the system keychain.
// Returns nil if the key was not found.
func Delete(key string) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- zkeyring.Delete(service, key)
	}()

	select {
	case <-ctx.Done():
		return fmt.Errorf("keychain timeout")
	case err := <-done:
		if errors.Is(err, zkeyring.ErrNotFound) {
			return nil
		}
		return err
	}
}

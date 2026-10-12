// Package keyringtest is a fake keychain for tests that is as strict as the
// real one: it refuses what go-keyring's macOS backend refuses.
package keyringtest

import (
	"encoding/base64"
	"fmt"
	"strings"
	"sync"
	"testing"

	gokeyring "github.com/zalando/go-keyring"

	"github.com/smeltdev/smelt/internal/keyring"
)

// MaxCommandLen is go-keyring's macOS limit on the `security` command it runs
// to store a secret (keyring_darwin.go).
const MaxCommandLen = 4096

// Fake is an in-memory keychain.
type Fake struct {
	mu    sync.Mutex
	items map[string]string
	sets  []string
}

// Install replaces the keychain with an empty Fake for the test.
func Install(t testing.TB) *Fake {
	t.Helper()
	f := &Fake{items: map[string]string{}}
	t.Cleanup(keyring.SetBackend(f))
	return f
}

// Set stores a secret, or fails with gokeyring.ErrSetDataTooBig when the
// command go-keyring would build is longer than MaxCommandLen.
func (f *Fake) Set(service, user, secret string) error {
	enc := "go-keyring-base64:" + base64.StdEncoding.EncodeToString([]byte(secret))
	cmd := fmt.Sprintf("add-generic-password -U -s %s -a %s -w %s\n", quote(service), quote(user), quote(enc))
	if len(cmd) > MaxCommandLen {
		return gokeyring.ErrSetDataTooBig
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.items[service+"\x00"+user] = secret
	f.sets = append(f.sets, service)
	return nil
}

// Get returns a stored secret.
func (f *Fake) Get(service, user string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.items[service+"\x00"+user]
	if !ok {
		return "", gokeyring.ErrNotFound
	}
	return v, nil
}

// Delete removes a secret.
func (f *Fake) Delete(service, user string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := service + "\x00" + user
	if _, ok := f.items[k]; !ok {
		return gokeyring.ErrNotFound
	}
	delete(f.items, k)
	return nil
}

// Sets lists the service of every successful Set, in the order they happened.
func (f *Fake) Sets() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.sets...)
}

// Services lists the services that hold a secret.
func (f *Fake) Services() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.items))
	for k := range f.items {
		out = append(out, strings.SplitN(k, "\x00", 2)[0])
	}
	return out
}

// quote approximates go-keyring's shell quoting: single quotes around the value.
func quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

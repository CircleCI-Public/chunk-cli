package keyring

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os/user"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/zalando/go-keyring"

	"github.com/smeltdev/smelt/internal/brand"
)

const (
	servicePrefix = brand.KeychainPrefix

	// SourceKeychain is the source label used when a value comes from the system keychain.
	SourceKeychain = "System keychain"

	opTimeout = 3 * time.Second

	// MaxPartSize is the most bytes SetLarge stores in one keychain entry.
	// go-keyring on macOS refuses a `security` command longer than 4,096
	// bytes, and that command holds the base64 of the secret (a third larger)
	// plus the service and account names and the command's own arguments.
	// 2,048 bytes becomes about 2,750 on the command line, which leaves room.
	MaxPartSize = 2048

	manifestPrefix = "smelt-chunks:v1:"
	manifestFamily = "smelt-chunks:"
)

// ServiceCircleCI returns the keychain service key for the given CircleCI base URL.
func ServiceCircleCI(baseURL string) string {
	return servicePrefix + "circleci:" + strings.TrimRight(baseURL, "/")
}

// ServiceAnthropic returns the keychain service key for the given Anthropic base URL.
func ServiceAnthropic(baseURL string) string {
	return servicePrefix + "anthropic:" + strings.TrimRight(baseURL, "/")
}

// ServiceAnthropicOAuth returns the keychain service key for the long-lived
// Claude subscription token used for the given Anthropic base URL.
func ServiceAnthropicOAuth(baseURL string) string {
	return servicePrefix + "anthropic-oauth:" + strings.TrimRight(baseURL, "/")
}

// Keychain services for the two ways to sign in to Codex.
const (
	// ServiceCodexChatGPT holds the contents of auth.json from a ChatGPT login.
	ServiceCodexChatGPT = servicePrefix + "codex:chatgpt"
	// ServiceCodexAPIKey holds an OpenAI API key for Codex.
	ServiceCodexAPIKey = servicePrefix + "codex:api-key"
)

// ServiceGitHub returns the keychain service key for the given GitHub API URL.
func ServiceGitHub(baseURL string) string {
	return servicePrefix + "github:" + strings.TrimRight(baseURL, "/")
}

// ErrNotFound is returned by Get when no credential is stored or the keyring is unavailable.
var ErrNotFound = errors.New("credential not found in keychain")

func username() string {
	u, err := user.Current()
	if err != nil {
		return brand.KeychainFallbackUser
	}
	return u.Username
}

// Backend is the keychain that Get, Set and Delete talk to.
type Backend interface {
	Set(service, user, secret string) error
	Get(service, user string) (string, error)
	Delete(service, user string) error
}

// goKeyring is the system keychain, through go-keyring.
type goKeyring struct{}

func (goKeyring) Set(service, user, secret string) error { return keyring.Set(service, user, secret) }
func (goKeyring) Get(service, user string) (string, error) {
	return keyring.Get(service, user)
}
func (goKeyring) Delete(service, user string) error { return keyring.Delete(service, user) }

// backend is a test seam: tests swap in a fake keychain with SetBackend.
var backend Backend = goKeyring{}

// SetBackend replaces the keychain and returns a function that restores the
// previous one. It exists for tests.
func SetBackend(b Backend) (restore func()) {
	prev := backend
	backend = b
	return func() { backend = prev }
}

// Get retrieves a credential. Returns ErrNotFound if absent or keyring unavailable.
func Get(service string) (string, error) {
	type result struct {
		val string
		err error
	}
	ch := make(chan result, 1)
	go func() {
		val, err := backend.Get(service, username())
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
		ch <- backend.Set(service, username(), secret)
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
		err := backend.Delete(service, username())
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

// ErrIncomplete is returned by GetLarge when a chunked credential is missing a
// part, has a part that was changed, or was written by an unknown version.
var ErrIncomplete = errors.New("the stored credential is incomplete; log in again")

func partService(service string, n int) string {
	return service + "#" + strconv.Itoa(n)
}

func digest(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// splitParts cuts secret into parts of at most MaxPartSize bytes, never in the
// middle of a UTF-8 character, since some keychains only hold valid text.
func splitParts(secret string) []string {
	var parts []string
	for len(secret) > MaxPartSize {
		cut := MaxPartSize
		for cut > 0 && !utf8.RuneStart(secret[cut]) {
			cut--
		}
		parts = append(parts, secret[:cut])
		secret = secret[cut:]
	}
	return append(parts, secret)
}

// parseManifest returns the part count and hash of a manifest.
func parseManifest(v string) (count int, sum string, err error) {
	rest, ok := strings.CutPrefix(v, manifestPrefix)
	if !ok {
		return 0, "", ErrIncomplete
	}
	n, sum, ok := strings.Cut(rest, ":")
	count, convErr := strconv.Atoi(n)
	if !ok || convErr != nil || count < 1 {
		return 0, "", ErrIncomplete
	}
	return count, sum, nil
}

// SetLarge stores a secret that may be too big for one keychain entry. A
// secret of up to MaxPartSize bytes is stored under service exactly as Set
// does. A larger one goes in parts under service#1, service#2, ... and a
// manifest under service. The parts are written first and the manifest last,
// so a reader never sees a manifest whose parts are not there yet.
func SetLarge(service, secret string) error {
	// Parts of a longer previous value must go, or they would linger forever.
	if err := deleteParts(service); err != nil {
		return err
	}
	if len(secret) <= MaxPartSize {
		return Set(service, secret)
	}
	parts := splitParts(secret)
	for i, p := range parts {
		if err := Set(partService(service, i+1), p); err != nil {
			return fmt.Errorf("store part %d of %d: %w", i+1, len(parts), err)
		}
	}
	manifest := fmt.Sprintf("%s%d:%s", manifestPrefix, len(parts), digest(secret))
	if err := Set(service, manifest); err != nil {
		return fmt.Errorf("store the manifest: %w", err)
	}
	return nil
}

// GetLarge reads a secret stored by SetLarge. It returns ErrNotFound when
// nothing is stored and ErrIncomplete when the parts do not add up to what was
// stored; never a partly read secret.
func GetLarge(service string) (string, error) {
	v, err := Get(service)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(v, manifestFamily) {
		return v, nil
	}
	count, sum, err := parseManifest(v)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for i := 1; i <= count; i++ {
		p, err := Get(partService(service, i))
		if err != nil {
			return "", ErrIncomplete
		}
		b.WriteString(p)
	}
	if digest(b.String()) != sum {
		return "", ErrIncomplete
	}
	return b.String(), nil
}

// DeleteLarge removes a secret stored by SetLarge, manifest and parts.
func DeleteLarge(service string) error {
	return errors.Join(Delete(service), deleteParts(service))
}

// deleteParts removes service#1, service#2, ... until one is absent. The
// manifest is read first so a gap left by a failed write cannot hide parts.
func deleteParts(service string) error {
	count := 0
	if v, err := Get(service); err == nil && strings.HasPrefix(v, manifestFamily) {
		count, _, _ = parseManifest(v)
	}
	var errs []error
	for i := 1; ; i++ {
		_, err := Get(partService(service, i))
		if err != nil && i > count {
			return errors.Join(errs...)
		}
		errs = append(errs, Delete(partService(service, i)))
	}
}

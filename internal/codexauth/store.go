package codexauth

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/smeltdev/smelt/internal/harness"
	"github.com/smeltdev/smelt/internal/harness/codex"
	"github.com/smeltdev/smelt/internal/keyring"
)

// Kind is how the user signed in to Codex.
type Kind string

// The ways to sign in.
const (
	KindChatGPT Kind = "chatgpt"
	KindAPIKey  Kind = "api-key"
)

// ErrNone is returned when there is no Codex credential at all.
var ErrNone = errors.New("no Codex credential found")

// Login is a Codex credential found on this machine.
type Login struct {
	Kind Kind
	// Auth is the full auth.json of a ChatGPT login, refresh token and all. It
	// stays on this machine; Credential is what a container may have.
	Auth []byte
	// APIKey is the key of an API-key login.
	APIKey string
	// FromEnv reports that the login came from the environment, so it cannot
	// be stored back after a renewal.
	FromEnv bool
}

// Find looks for a credential: the environment first, then the keychain. A
// ChatGPT login wins over an API key at each level.
func Find() (Login, error) {
	if v := os.Getenv(codex.EnvAuthJSON); v != "" {
		raw, err := base64.StdEncoding.DecodeString(v)
		if err != nil {
			return Login{}, fmt.Errorf("%s is not base64: %w", codex.EnvAuthJSON, err)
		}
		return Login{Kind: KindChatGPT, Auth: raw, FromEnv: true}, nil
	}
	if v := os.Getenv(codex.EnvAPIKey); v != "" {
		return Login{Kind: KindAPIKey, APIKey: v, FromEnv: true}, nil
	}
	if v, err := keyring.Get(keyring.ServiceCodexChatGPT); err == nil && v != "" {
		return Login{Kind: KindChatGPT, Auth: []byte(v)}, nil
	}
	if v, err := keyring.Get(keyring.ServiceCodexAPIKey); err == nil && v != "" {
		return Login{Kind: KindAPIKey, APIKey: v}, nil
	}
	return Login{}, ErrNone
}

// Credential is what a run's sidecars get: for a ChatGPT login the base64 of
// the container copy of auth.json, for an API key the key.
func (l Login) Credential() (harness.Credential, error) {
	if l.Kind == KindAPIKey {
		return harness.Credential{EnvVar: codex.EnvAPIKey, Value: l.APIKey}, nil
	}
	cp, err := ContainerCopy(l.Auth)
	if err != nil {
		return harness.Credential{}, err
	}
	return harness.Credential{EnvVar: codex.EnvAuthJSON, Value: base64.StdEncoding.EncodeToString(cp)}, nil
}

// StoreChatGPT keeps the contents of auth.json in the keychain.
func StoreChatGPT(raw []byte) error {
	if _, err := Expiry(raw); err != nil {
		return err
	}
	return keyring.Set(keyring.ServiceCodexChatGPT, string(raw))
}

// StoreAPIKey keeps an OpenAI API key in the keychain.
func StoreAPIKey(key string) error {
	if strings.TrimSpace(key) == "" {
		return errors.New("the API key is empty")
	}
	return keyring.Set(keyring.ServiceCodexAPIKey, strings.TrimSpace(key))
}

// Remove deletes both stored Codex credentials.
func Remove() error {
	return errors.Join(keyring.Delete(keyring.ServiceCodexChatGPT), keyring.Delete(keyring.ServiceCodexAPIKey))
}

// ForRun finds the credential for a run and makes sure a ChatGPT login lasts
// it, renewing and storing it when it is close to expiry. The result is the
// credential for the run's sidecars.
func ForRun(ctx context.Context, host Host, now time.Time) (harness.Credential, error) {
	l, err := Find()
	if err != nil {
		return harness.Credential{}, err
	}
	if l.Kind == KindChatGPT {
		raw, renewed, err := Ensure(ctx, l.Auth, now, host.Renew)
		if err != nil {
			return harness.Credential{}, err
		}
		if renewed && !l.FromEnv {
			if err := StoreChatGPT(raw); err != nil {
				return harness.Credential{}, fmt.Errorf("store the renewed login: %w", err)
			}
		}
		l.Auth = raw
	}
	return l.Credential()
}

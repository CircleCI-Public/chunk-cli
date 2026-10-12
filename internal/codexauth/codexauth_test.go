package codexauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/smeltdev/smelt/internal/harness/codex"
	"github.com/smeltdev/smelt/internal/keyring"
	"github.com/smeltdev/smelt/internal/keyring/keyringtest"
)

var now = time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)

// jwt builds an unsigned token that expires at exp.
func jwt(exp time.Time) string {
	enc := base64.RawURLEncoding.EncodeToString
	payload, _ := json.Marshal(map[string]any{"exp": exp.Unix()})
	return enc([]byte(`{"alg":"none"}`)) + "." + enc(payload) + ".sig"
}

// authJSON is a ChatGPT login whose access token expires at exp.
func authJSON(exp time.Time) []byte {
	b, _ := json.Marshal(map[string]any{
		"OPENAI_API_KEY": nil, "auth_mode": "chatgpt", "last_refresh": "2026-10-10T00:00:00Z",
		"tokens": map[string]any{
			"access_token": jwt(exp), "id_token": "id-token-secret", "refresh_token": "refresh-secret", "account_id": "acct-1",
		},
	})
	return b
}

// fakeCodex writes a codex script and returns a Host that runs it.
func fakeCodex(t *testing.T, body string) Host {
	t.Helper()
	path := filepath.Join(t.TempDir(), "codex")
	assert.NilError(t, os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755))
	return Host{Binary: path}
}

func TestContainerCopyBlanksOnlyTheRefreshToken(t *testing.T) {
	raw := authJSON(now.Add(240 * time.Hour))
	cp, err := ContainerCopy(raw)
	assert.NilError(t, err)
	assert.Check(t, !strings.Contains(string(cp), "refresh-secret"))
	assert.Check(t, cmp.Contains(string(cp), `"refresh_token":""`))

	var before, after map[string]any
	assert.NilError(t, json.Unmarshal(raw, &before))
	assert.NilError(t, json.Unmarshal(cp, &after))
	before["tokens"].(map[string]any)["refresh_token"] = ""
	assert.DeepEqual(t, before, after)
}

func TestClassify(t *testing.T) {
	tests := []struct {
		name string
		exp  time.Time
		want State
	}{
		{"ten days", now.Add(240 * time.Hour), Fresh},
		{"just over a day", now.Add(25 * time.Hour), Fresh},
		{"under a day", now.Add(23 * time.Hour), Soon},
		{"expired", now.Add(-time.Minute), Expired},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exp, err := Expiry(authJSON(tt.exp))
			assert.NilError(t, err)
			assert.Equal(t, Classify(exp, now), tt.want)
		})
	}
}

func TestEnsure(t *testing.T) {
	fresh := authJSON(now.Add(240 * time.Hour))
	renewedTo := func(raw []byte, changed bool) func(context.Context, []byte) ([]byte, bool, error) {
		return func(context.Context, []byte) ([]byte, bool, error) { return raw, changed, nil }
	}
	tests := []struct {
		name    string
		login   []byte
		renew   func(context.Context, []byte) ([]byte, bool, error)
		wantErr error
		renewed bool
	}{
		{"fresh needs no renewal", fresh, nil, nil, false},
		{"soon and renewed", authJSON(now.Add(time.Hour)), renewedTo(fresh, true), nil, true},
		{"soon and not renewed", authJSON(now.Add(time.Hour)), renewedTo(nil, false), ErrExpiresSoon, false},
		{"expired and not renewed", authJSON(now.Add(-time.Hour)), renewedTo(nil, false), ErrExpired, false},
		{"soon and renewal failed", authJSON(now.Add(time.Hour)), func(context.Context, []byte) ([]byte, bool, error) {
			return nil, false, errors.New("boom")
		}, ErrExpiresSoon, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, renewed, err := Ensure(t.Context(), tt.login, now, tt.renew)
			if tt.wantErr != nil {
				assert.ErrorIs(t, err, tt.wantErr)
				return
			}
			assert.NilError(t, err)
			assert.Equal(t, renewed, tt.renewed)
			assert.Assert(t, len(out) > 0)
		})
	}
}

func TestRenewReadsTheFileCodexRewrote(t *testing.T) {
	old := authJSON(time.Now().Add(time.Hour))
	newer := authJSON(time.Now().Add(240 * time.Hour))
	newPath := filepath.Join(t.TempDir(), "new.json")
	assert.NilError(t, os.WriteFile(newPath, newer, 0o600))

	rewrites := fakeCodex(t, `cp `+newPath+` "$CODEX_HOME/auth.json"`)
	got, changed, err := rewrites.Renew(t.Context(), old)
	assert.NilError(t, err)
	assert.Assert(t, changed)
	assert.DeepEqual(t, got, newer)

	leaves := fakeCodex(t, `exit 0`)
	_, changed, err = leaves.Renew(t.Context(), old)
	assert.NilError(t, err)
	assert.Assert(t, !changed)
}

func TestLoginStoresNothingOutsideItsPrivateHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", filepath.Join(home, "user-codex"))
	raw := authJSON(now.Add(240 * time.Hour))
	src := filepath.Join(t.TempDir(), "auth.json")
	assert.NilError(t, os.WriteFile(src, raw, 0o600))

	h := fakeCodex(t, `[ "$1" = login ] || exit 2
case "$CODEX_HOME" in *user-codex*) exit 3;; esac
cp `+src+` "$CODEX_HOME/auth.json"; echo "$CODEX_HOME" > `+filepath.Join(home, "home-used"))
	got, err := h.Login(t.Context(), LoginOptions{})
	assert.NilError(t, err)
	assert.DeepEqual(t, got, raw)

	used, err := os.ReadFile(filepath.Join(home, "home-used"))
	assert.NilError(t, err)
	_, err = os.Stat(strings.TrimSpace(string(used)))
	assert.Assert(t, os.IsNotExist(err), "the private CODEX_HOME is removed")
}

func TestLoginExplainsADisabledDeviceCode(t *testing.T) {
	h := fakeCodex(t, `echo "device code sign-in is disabled for this workspace" >&2; exit 1`)
	_, err := h.Login(t.Context(), LoginOptions{DeviceAuth: true})
	assert.ErrorIs(t, err, ErrDeviceCodeDisabled)
}

func TestKeychainRoundTripForBothKinds(t *testing.T) {
	keyringtest.Install(t)
	t.Setenv(codex.EnvAuthJSON, "")
	t.Setenv(codex.EnvAPIKey, "")

	_, err := Find()
	assert.ErrorIs(t, err, ErrNone)

	assert.NilError(t, StoreAPIKey("sk-test-key"))
	l, err := Find()
	assert.NilError(t, err)
	assert.Equal(t, l.Kind, KindAPIKey)
	cred, err := l.Credential()
	assert.NilError(t, err)
	assert.Equal(t, cred.EnvVar, codex.EnvAPIKey)
	assert.Equal(t, cred.Value, "sk-test-key")

	// A ChatGPT login wins over a key.
	raw := authJSON(now.Add(240 * time.Hour))
	assert.NilError(t, StoreChatGPT(raw))
	l, err = Find()
	assert.NilError(t, err)
	assert.Equal(t, l.Kind, KindChatGPT)
	assert.DeepEqual(t, l.Auth, raw)
	cred, err = l.Credential()
	assert.NilError(t, err)
	assert.Equal(t, cred.EnvVar, codex.EnvAuthJSON)
	sent, err := base64.StdEncoding.DecodeString(cred.Value)
	assert.NilError(t, err)
	assert.Check(t, !strings.Contains(string(sent), "refresh-secret"), "no refresh token enters a container")
	assert.Check(t, cmp.Contains(string(sent), `"refresh_token":""`))

	assert.NilError(t, Remove())
	_, err = Find()
	assert.ErrorIs(t, err, ErrNone)
}

func TestEnvironmentOverridesTheKeychain(t *testing.T) {
	keyringtest.Install(t)
	assert.NilError(t, StoreAPIKey("stored"))
	t.Setenv(codex.EnvAuthJSON, base64.StdEncoding.EncodeToString(authJSON(now.Add(240*time.Hour))))
	l, err := Find()
	assert.NilError(t, err)
	assert.Equal(t, l.Kind, KindChatGPT)
	assert.Assert(t, l.FromEnv)

	t.Setenv(codex.EnvAuthJSON, "")
	t.Setenv(codex.EnvAPIKey, "from-env")
	l, err = Find()
	assert.NilError(t, err)
	assert.Equal(t, l.APIKey, "from-env")
}

func TestForRunRenewsAndStoresALoginThatExpiresSoon(t *testing.T) {
	keyringtest.Install(t)
	t.Setenv(codex.EnvAuthJSON, "")
	t.Setenv(codex.EnvAPIKey, "")
	soon := authJSON(time.Now().Add(time.Hour))
	assert.NilError(t, StoreChatGPT(soon))

	newer := authJSON(time.Now().Add(240 * time.Hour))
	newPath := filepath.Join(t.TempDir(), "new.json")
	assert.NilError(t, os.WriteFile(newPath, newer, 0o600))
	h := fakeCodex(t, `cp `+newPath+` "$CODEX_HOME/auth.json"`)

	cred, err := ForRun(t.Context(), h, time.Now())
	assert.NilError(t, err)
	assert.Equal(t, cred.EnvVar, codex.EnvAuthJSON)
	l, err := Find()
	assert.NilError(t, err)
	assert.DeepEqual(t, l.Auth, newer)

	// A login that cannot be renewed stops the run, with the way out.
	assert.NilError(t, StoreChatGPT(soon))
	_, err = ForRun(t.Context(), fakeCodex(t, `exit 0`), time.Now())
	assert.ErrorIs(t, err, ErrExpiresSoon)
	assert.ErrorContains(t, err, "smelt refresh codex")
}

func TestProbeReadsTheRecordedAppServerReplies(t *testing.T) {
	fixture, err := filepath.Abs("../harness/testdata/real/codex-appserver-account.jsonl")
	assert.NilError(t, err)
	h := fakeCodex(t, `[ "$1" = app-server ] || exit 2
cat "$CODEX_HOME/auth.json" > /dev/null || exit 3
cat `+fixture)
	acct := h.Probe(t.Context(), authJSON(now.Add(240*time.Hour)), now)
	assert.Equal(t, acct.Auth, AuthOK)
	assert.Equal(t, acct.Plan, "free")
	assert.Equal(t, len(acct.Windows), 1)
	assert.Equal(t, acct.Windows[0].Window, "30d")
	assert.Equal(t, *acct.Windows[0].UsedPercent, 0.0)
	assert.Equal(t, acct.Windows[0].ResetsAt.Unix(), int64(1794352576))
	assert.Equal(t, acct.Windows[0].Source, SourceAppServer)
}

func TestProbeNamesWindowsByTheirLength(t *testing.T) {
	replies := `{"id":2,"result":{"account":{"type":"chatgpt","planType":"plus"}}}
{"id":3,"result":{"rateLimits":{"primary":{"usedPercent":42,"windowDurationMins":300,"resetsAt":1790000000},"secondary":{"usedPercent":18,"windowDurationMins":10080,"resetsAt":1790500000}}}}
`
	h := fakeCodex(t, "cat <<'EOF'\n"+replies+"EOF\n")
	acct := h.Probe(t.Context(), authJSON(now.Add(240*time.Hour)), now)
	assert.Equal(t, acct.Plan, "plus")
	assert.Equal(t, len(acct.Windows), 2)
	assert.Equal(t, acct.Windows[0].Window, "5h")
	assert.Equal(t, *acct.Windows[0].UsedPercent, 42.0)
	assert.Equal(t, acct.Windows[1].Window, "7d")
}

func TestProbeReportsAMissingLogin(t *testing.T) {
	replies := `{"id":2,"error":{"code":-32600,"message":"codex account authentication required to read account"}}
{"id":3,"error":{"code":-32600,"message":"codex account authentication required to read rate limits"}}
`
	h := fakeCodex(t, "cat <<'EOF'\n"+replies+"EOF\n")
	assert.Equal(t, h.Probe(t.Context(), authJSON(now.Add(240*time.Hour)), now).Auth, AuthMissing)
	assert.Equal(t, h.Probe(t.Context(), nil, now).Auth, AuthMissing)
}

func TestProbeNeverSendsTheRefreshToken(t *testing.T) {
	out := filepath.Join(t.TempDir(), "seen.json")
	h := fakeCodex(t, `cp "$CODEX_HOME/auth.json" `+out)
	h.Probe(t.Context(), authJSON(now.Add(240*time.Hour)), now)
	seen, err := os.ReadFile(out)
	assert.NilError(t, err)
	assert.Check(t, !strings.Contains(string(seen), "refresh-secret"))
}

func TestProbeDoesNotWaitForAServerThatStaysOpen(t *testing.T) {
	replies := `{"id":2,"result":{"account":{"type":"chatgpt","planType":"plus"}}}
{"id":3,"result":{"rateLimits":{"primary":{"usedPercent":1,"windowDurationMins":300,"resetsAt":1790000000}}}}
`
	// Like the real app-server, it only exits once its stdin closes.
	h := fakeCodex(t, "cat <<'EOF'\n"+replies+"EOF\ncat > /dev/null\n")
	done := make(chan Account, 1)
	go func() { done <- h.Probe(t.Context(), authJSON(now.Add(240*time.Hour)), now) }()
	select {
	case acct := <-done:
		assert.Equal(t, acct.Plan, "plus")
		assert.Equal(t, len(acct.Windows), 1)
	case <-time.After(10 * time.Second):
		t.Fatal("Probe waited for the server instead of returning once both replies arrived")
	}
}

// realisticAuthJSON is an auth.json the size of a real one: three JWT-sized
// tokens, 5–6 KB in all. It is built here; never commit a real one.
func realisticAuthJSON(t *testing.T) []byte {
	t.Helper()
	enc := base64.RawURLEncoding.EncodeToString
	payload, _ := json.Marshal(map[string]any{"exp": time.Now().Add(240 * time.Hour).Unix()})
	jwt := func(pad int) string {
		return enc([]byte(`{"alg":"RS256"}`)) + "." + enc(payload) + "." + strings.Repeat("s", pad)
	}
	b, err := json.Marshal(map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"access_token": jwt(1800), "id_token": jwt(1800), "refresh_token": jwt(1800),
		},
	})
	assert.NilError(t, err)
	assert.Assert(t, len(b) > 5000 && len(b) < 7000, len(b))
	return b
}

func TestStoreChatGPTHandlesARealisticAuthJSON(t *testing.T) {
	fake := keyringtest.Install(t)
	t.Setenv("SMELT_CODEX_AUTH_JSON", "")
	t.Setenv("CODEX_API_KEY", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	raw := realisticAuthJSON(t)
	assert.NilError(t, StoreChatGPT(raw))
	assert.Assert(t, len(fake.Services()) > 1, "stored in parts")
	l, err := Find()
	assert.NilError(t, err)
	assert.DeepEqual(t, l.Auth, raw)

	assert.NilError(t, Remove())
	assert.Equal(t, len(fake.Services()), 0)
}

func TestStoreChatGPTSaysTheLoginSucceeded(t *testing.T) {
	keyringtest.Install(t)
	restore := keyring.SetBackend(failingBackend{})
	defer restore()
	err := StoreChatGPT(realisticAuthJSON(t))
	assert.ErrorContains(t, err, "Codex login succeeded, but smelt could not save it in the keychain")
}

type failingBackend struct{}

func (failingBackend) Set(string, string, string) error   { return errors.New("locked") }
func (failingBackend) Get(string, string) (string, error) { return "", errors.New("locked") }
func (failingBackend) Delete(string, string) error        { return errors.New("locked") }

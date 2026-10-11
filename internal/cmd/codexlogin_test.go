package cmd

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gokeyring "github.com/zalando/go-keyring"
	"gotest.tools/v3/assert"

	"github.com/smeltdev/smelt/internal/codexauth"
	"github.com/smeltdev/smelt/internal/config"
	"github.com/smeltdev/smelt/internal/iostream"
)

func testAuthJSON(t *testing.T) []byte {
	t.Helper()
	enc := base64.RawURLEncoding.EncodeToString
	payload, _ := json.Marshal(map[string]any{"exp": time.Now().Add(240 * time.Hour).Unix()})
	b, err := json.Marshal(map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"access_token": enc([]byte(`{}`)) + "." + enc(payload) + ".sig", "refresh_token": "refresh-secret", "id_token": "id-secret",
		},
	})
	assert.NilError(t, err)
	return b
}

func stubCodex(t *testing.T, body string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "codex")
	assert.NilError(t, os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755))
	old := codexHost
	codexHost = codexauth.Host{Binary: path}
	t.Cleanup(func() { codexHost = old })
}

func TestAuthLoginCodexStoresTheLoginInTheKeychainOnly(t *testing.T) {
	isolateConfig(t)
	gokeyring.MockInit()
	t.Setenv("SMELT_CODEX_AUTH_JSON", "")
	t.Setenv("CODEX_API_KEY", "")
	raw := testAuthJSON(t)
	src := filepath.Join(t.TempDir(), "auth.json")
	assert.NilError(t, os.WriteFile(src, raw, 0o600))
	stubCodex(t, `[ "$1" = login ] || exit 2; cp `+src+` "$CODEX_HOME/auth.json"`)
	oldInteractive := isInteractive
	isInteractive = func() bool { return true }
	t.Cleanup(func() { isInteractive = oldInteractive })
	stops := 0
	oldStop := stopDaemonForCredentialChange
	stopDaemonForCredentialChange = func() bool { stops++; return false }
	t.Cleanup(func() { stopDaemonForCredentialChange = oldStop })

	out, err := runRoot(t, "auth", "login", "codex")
	assert.NilError(t, err, out)
	assert.Equal(t, stops, 1)
	l, err := codexauth.Find()
	assert.NilError(t, err)
	assert.DeepEqual(t, l.Auth, raw)
	for _, secret := range []string{"refresh-secret", "id-secret"} {
		assert.Assert(t, !strings.Contains(out, secret), out)
	}
	_, err = os.Stat(filepath.Join(os.Getenv(config.EnvHome), ".codex"))
	assert.Assert(t, os.IsNotExist(err), "~/.codex is never touched")
}

func TestAuthLoginRejectsAnUnknownProvider(t *testing.T) {
	isolateConfig(t)
	_, err := runRoot(t, "auth", "login", "gemini")
	assert.ErrorContains(t, err, "gemini")
}

func TestAuthLoginCodexNeedsATerminal(t *testing.T) {
	isolateConfig(t)
	oldInteractive := isInteractive
	isInteractive = func() bool { return false }
	t.Cleanup(func() { isInteractive = oldInteractive })
	_, err := runRoot(t, "auth", "login", "codex")
	assert.ErrorContains(t, err, "interactive terminal")
}

func TestAuthStatusAndRemoveCodex(t *testing.T) {
	isolateConfig(t)
	gokeyring.MockInit()
	t.Setenv("SMELT_CODEX_AUTH_JSON", "")
	t.Setenv("CODEX_API_KEY", "")
	assert.NilError(t, codexauth.StoreChatGPT(testAuthJSON(t)))
	stubCodex(t, `exit 1`)

	var out bytes.Buffer
	codexStatus(t.Context(), iostream.Streams{Out: &out, Err: &out})
	assert.Assert(t, strings.Contains(out.String(), "ChatGPT login"), out.String())
	for _, secret := range []string{"refresh-secret", "id-secret", "access_token"} {
		assert.Assert(t, !strings.Contains(out.String(), secret), out.String())
	}

	removed, err := runRoot(t, "auth", "remove", "codex", "--force")
	assert.NilError(t, err, removed)
	_, err = codexauth.Find()
	assert.ErrorIs(t, err, codexauth.ErrNone)
}

func TestDecideInitHarness(t *testing.T) {
	tests := []struct {
		name string
		in   initHarnessState
		want initHarnessAction
	}{
		{"skip wins", initHarnessState{Skip: true, Flag: "codex", Interactive: true}, harnessStepSkipped},
		{"flag wins in any mode", initHarnessState{Flag: "codex", Saved: "claude"}, harnessStepFromFlag},
		{"a saved agent is described", initHarnessState{Saved: "claude", Interactive: true}, harnessStepReport},
		{"a terminal is asked", initHarnessState{Interactive: true}, harnessStepAsk},
		{"no terminal and nothing to say", initHarnessState{}, harnessStepSkipped},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, decideInitHarness(tt.in), tt.want)
		})
	}
}

func TestInitHarnessStepSavesTheFlagAndKeepsOtherSettings(t *testing.T) {
	cfg := &config.ProjectConfig{Factory: &config.FactoryConfig{Backend: "docker"}}
	var out bytes.Buffer
	changed := initHarnessStep(iostream.Streams{Out: &out, Err: &out}, cfg, initFactoryOptions{Harness: "codex"})
	assert.Assert(t, changed)
	assert.Equal(t, cfg.Factory.Harness, "codex")
	assert.Equal(t, cfg.Factory.Backend, "docker")
	assert.Assert(t, !initHarnessStep(iostream.Streams{Out: &out, Err: &out}, cfg, initFactoryOptions{Harness: "codex"}), "unchanged")
}

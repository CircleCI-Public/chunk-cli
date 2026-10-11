package codexauth

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Binary is the codex executable on the host.
const Binary = "codex"

// authFile is the credential inside CODEX_HOME.
const authFile = "auth.json"

// refreshTimeout bounds the one-turn run that makes codex renew its tokens.
const refreshTimeout = 2 * time.Minute

// ErrExpiresSoon is returned when a login is too close to expiry to start a
// run and could not be renewed on this machine.
var ErrExpiresSoon = errors.New("your Codex login expires soon; run `smelt refresh codex`")

// ErrExpired is ErrExpiresSoon for a login that has already expired.
var ErrExpired = errors.New("your Codex login has expired; run `smelt refresh codex`")

// ErrDeviceCodeDisabled is returned when ChatGPT refuses device-code sign-in.
var ErrDeviceCodeDisabled = errors.New("device-code sign-in is not enabled for your ChatGPT account: turn it on under ChatGPT → Settings → Security, or sign in without --device-auth")

// Host drives the codex CLI on this machine. Binary is its path; "codex" when
// empty. Every call uses a private CODEX_HOME, so ~/.codex is never touched.
type Host struct {
	Binary string
}

func (h Host) binary() string {
	if h.Binary == "" {
		return Binary
	}
	return h.Binary
}

// LoginOptions shape Login.
type LoginOptions struct {
	// DeviceAuth signs in with a code instead of a browser.
	DeviceAuth bool
	Stdin      io.Reader
	Stdout     io.Writer
	Stderr     io.Writer
}

// Login runs `codex login` against a private CODEX_HOME and returns the
// auth.json it wrote. The directory is removed before returning.
func (h Host) Login(ctx context.Context, opts LoginOptions) ([]byte, error) {
	home, err := os.MkdirTemp("", "smelt-codex-login-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(home) }()

	args := []string{"login"}
	if opts.DeviceAuth {
		args = append(args, "--device-auth")
	}
	var seen bytes.Buffer
	cmd := exec.CommandContext(ctx, h.binary(), args...)
	cmd.Env = withCodexHome(home)
	cmd.Stdin = opts.Stdin
	cmd.Stdout = io.MultiWriter(orDiscard(opts.Stdout), &seen)
	cmd.Stderr = io.MultiWriter(orDiscard(opts.Stderr), &seen)
	if err := cmd.Run(); err != nil {
		if opts.DeviceAuth && deviceCodeDisabled(seen.String()) {
			return nil, ErrDeviceCodeDisabled
		}
		return nil, fmt.Errorf("codex login: %w", err)
	}
	raw, err := os.ReadFile(filepath.Join(home, authFile))
	if err != nil {
		return nil, fmt.Errorf("codex login wrote no %s: %w", authFile, err)
	}
	if _, err := Expiry(raw); err != nil {
		return nil, fmt.Errorf("codex login: %w", err)
	}
	return raw, nil
}

func orDiscard(w io.Writer) io.Writer {
	if w == nil {
		return io.Discard
	}
	return w
}

func deviceCodeDisabled(out string) bool {
	out = strings.ToLower(out)
	return strings.Contains(out, "device") && (strings.Contains(out, "disabled") || strings.Contains(out, "not enabled"))
}

// withCodexHome is the environment with CODEX_HOME pointed at home, and no
// variable that would make codex use an API key instead of the file.
func withCodexHome(home string) []string {
	var env []string
	for _, kv := range os.Environ() {
		switch name, _, _ := strings.Cut(kv, "="); name {
		case "CODEX_HOME", "CODEX_API_KEY", "OPENAI_API_KEY":
		default:
			env = append(env, kv)
		}
	}
	return append(env, "CODEX_HOME="+home)
}

// Renew makes codex refresh the tokens in raw, the full auth.json, by running
// one turn against a private CODEX_HOME that holds it, and returns the file as
// codex left it. changed is false when the access token is the same, which is
// what a login codex would not renew looks like.
func (h Host) Renew(ctx context.Context, raw []byte) (renewed []byte, changed bool, err error) {
	home, err := os.MkdirTemp("", "smelt-codex-refresh-")
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = os.RemoveAll(home) }()
	path := filepath.Join(home, authFile)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return nil, false, err
	}
	ctx, cancel := context.WithTimeout(ctx, refreshTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, h.binary(), "exec", "--json", "--skip-git-repo-check", "reply ok")
	cmd.Env = withCodexHome(home)
	cmd.Dir = home
	// The run's output may quote anything; none of it is kept.
	if err := cmd.Run(); err != nil {
		return raw, false, nil
	}
	after, err := os.ReadFile(path)
	if err != nil {
		return raw, false, nil
	}
	oldTok, err1 := AccessToken(raw)
	newTok, err2 := AccessToken(after)
	if err1 != nil || err2 != nil || oldTok == newTok {
		return raw, false, nil
	}
	return after, true, nil
}

// Ensure returns a login that is good for a run: raw itself when it has more
// than RenewWithin left, otherwise the result of renewing it with renew. renewed
// reports that the returned file differs from raw and should be stored. When
// the login cannot be made good it returns ErrExpiresSoon or ErrExpired.
func Ensure(ctx context.Context, raw []byte, now time.Time, renew func(context.Context, []byte) ([]byte, bool, error)) (out []byte, renewed bool, err error) {
	exp, err := Expiry(raw)
	if err != nil {
		return nil, false, err
	}
	state := Classify(exp, now)
	if state == Fresh {
		return raw, false, nil
	}
	if renew != nil {
		if after, changed, err := renew(ctx, raw); err == nil && changed {
			if exp, err := Expiry(after); err == nil && Classify(exp, now) == Fresh {
				return after, true, nil
			}
		}
	}
	if state == Expired {
		return nil, false, ErrExpired
	}
	return nil, false, ErrExpiresSoon
}

// BinaryPath is the codex executable to run.
func (h Host) BinaryPath() string { return h.binary() }

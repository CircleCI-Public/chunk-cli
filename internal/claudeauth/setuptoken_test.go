package claudeauth

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
)

const fakeToken = "sk-ant-oat01-AbCdEfGhIjKlMnOpQrStUvWxYz0123456789"

// fakeClaude puts a claude script on PATH that runs body.
func fakeClaude(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\n" + body + "\n"
	assert.NilError(t, os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o700))
	t.Setenv("PATH", dir)
}

func noPrompt(t *testing.T) func(string) (string, error) {
	t.Helper()
	return func(string) (string, error) {
		t.Fatal("prompter called when stdout held a token")
		return "", nil
	}
}

func TestSetupTokenCapturesTheToken(t *testing.T) {
	fakeClaude(t, "echo 'Visit https://claude.ai/oauth to authorize'\necho "+fakeToken)

	var out strings.Builder
	token, err := SetupToken(context.Background(), iostream.Streams{Err: &out}, noPrompt(t))
	assert.NilError(t, err)
	assert.Equal(t, token, fakeToken)

	// The authorize URL must reach the terminal; the token must not.
	assert.Assert(t, strings.Contains(out.String(), "https://claude.ai/oauth"), "echoed: %q", out.String())
	assert.Assert(t, !strings.Contains(out.String(), fakeToken), "echoed: %q", out.String())
	assert.Assert(t, strings.Contains(out.String(), "[token redacted]"), "echoed: %q", out.String())
}

func TestSetupTokenPromptsWhenStdoutHasNoToken(t *testing.T) {
	fakeClaude(t, "echo 'nothing useful here'")

	var out strings.Builder
	token, err := SetupToken(context.Background(), iostream.Streams{Err: &out}, func(label string) (string, error) {
		assert.Equal(t, label, "Token")
		return fakeToken, nil
	})
	assert.NilError(t, err)
	assert.Equal(t, token, fakeToken)
}

// A token on a line with no trailing newline still has to be found, since the
// flow can leave the cursor on it.
func TestSetupTokenCapturesAnUnterminatedLine(t *testing.T) {
	fakeClaude(t, "printf %s "+fakeToken)

	var out strings.Builder
	token, err := SetupToken(context.Background(), iostream.Streams{Err: &out}, noPrompt(t))
	assert.NilError(t, err)
	assert.Equal(t, token, fakeToken)
	assert.Assert(t, !strings.Contains(out.String(), fakeToken), "echoed: %q", out.String())
}

func TestSetupTokenFailedFlowWithNoToken(t *testing.T) {
	fakeClaude(t, "echo 'could not reach claude.ai' >&2\nexit 1")

	var out strings.Builder
	_, err := SetupToken(context.Background(), iostream.Streams{Err: &out}, noPrompt(t))
	assert.ErrorContains(t, err, "claude setup-token")
}

// A token printed before a non-zero exit is still a token.
func TestSetupTokenKeepsATokenFromAFailedRun(t *testing.T) {
	fakeClaude(t, "echo "+fakeToken+"\nexit 1")

	var out strings.Builder
	token, err := SetupToken(context.Background(), iostream.Streams{Err: &out}, noPrompt(t))
	assert.NilError(t, err)
	assert.Equal(t, token, fakeToken)
}

func TestSetupTokenClaudeNotInstalled(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	_, err := SetupToken(context.Background(), iostream.Streams{Err: &strings.Builder{}}, noPrompt(t))
	assert.Assert(t, errors.Is(err, ErrNotInstalled), "got %v", err)
}

func TestRedactWriterReportsEveryByteConsumed(t *testing.T) {
	var out strings.Builder
	r := &redactWriter{w: &out}

	in := []byte("one\ntwo\nthree")
	n, err := r.Write(in)
	assert.NilError(t, err)
	assert.Equal(t, n, len(in))
	assert.NilError(t, r.Flush())
	assert.Equal(t, out.String(), "one\ntwo\nthree")
}

// A token split across two writes still has to be redacted, so the writer
// holds back a partial line from where a token could start.
func TestRedactWriterRedactsAcrossWrites(t *testing.T) {
	var out strings.Builder
	r := &redactWriter{w: &out}

	_, err := r.Write([]byte(fakeToken[:10]))
	assert.NilError(t, err)
	_, err = r.Write([]byte(fakeToken[10:] + "\n"))
	assert.NilError(t, err)
	assert.NilError(t, r.Flush())

	assert.Equal(t, out.String(), "[token redacted]\n")
}

// A prompt with no newline has to reach the terminal while claude waits on
// stdin, not only once claude exits.
func TestRedactWriterForwardsAPromptBeforeItsNewline(t *testing.T) {
	var out strings.Builder
	r := &redactWriter{w: &out}

	_, err := r.Write([]byte("Paste code here > "))
	assert.NilError(t, err)
	assert.Equal(t, out.String(), "Paste code here > ")
}

func TestRedactWriterHoldsBackAPossibleTokenStart(t *testing.T) {
	var out strings.Builder
	r := &redactWriter{w: &out}

	_, err := r.Write([]byte("token: sk-an"))
	assert.NilError(t, err)
	assert.Equal(t, out.String(), "token: ")

	_, err = r.Write([]byte(fakeToken[len("sk-an"):] + "\n"))
	assert.NilError(t, err)
	assert.Equal(t, out.String(), "token: [token redacted]\n")
}

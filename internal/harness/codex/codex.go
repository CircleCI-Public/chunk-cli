// Package codex drives OpenAI Codex (codex exec) as a harness.
package codex

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/smeltdev/smelt/internal/brand"
	"github.com/smeltdev/smelt/internal/harness"
	"github.com/smeltdev/smelt/internal/sidecar"
)

// ErrMissing is returned when a sidecar has no codex binary.
var ErrMissing = errors.New("codex is not installed on the sidecar")

// msgHostMissing is a user-facing sentence, so it starts with the product
// name's capital. Tooling that watches runs can key off ErrHostMissing.
const msgHostMissing = "Codex can't run commands in this sandbox image: rebuild it with `" + brand.Command + " factory image --update-kit`"

// ErrHostMissing is returned when Codex reports that its command host,
// codex-code-mode-host, is missing or would not start: every command it tries
// fails, so the turn can do no work. Older images (kit v2) lack the host.
var ErrHostMissing = errors.New(msgHostMissing) //nolint:staticcheck // a user-facing sentence, which starts with the product name

// ErrCredentialRejected is returned when OpenAI rejects the credential.
var ErrCredentialRejected = errors.New("openai rejected the credential")

// Environment variables and paths on the sidecar.
const (
	// EnvAuthJSON carries the base64 of auth.json to Prepare, and only to it.
	EnvAuthJSON = "SMELT_CODEX_AUTH_JSON"
	// EnvAPIKey is the variable codex reads an API key from.
	EnvAPIKey = "CODEX_API_KEY" //nolint:gosec // the name of a variable, not a credential

	// codexHome is under the sandbox user's home: Codex refuses helper
	// binaries that live under /tmp.
	codexHome = "/home/circleci/.smelt-codex"
	guardDir  = "/tmp/smelt-guard"
)

const stderrTail = 2000

var credentialRejectedRe = regexp.MustCompile(`(?i)(401 unauthorized|not logged in|invalid api key|incorrect api key|authentication required)`)

// guardedGit are the git commands that would move the implementer's work out
// of the working tree the loop collects it from.
var guardedGit = []string{"commit", "stash", "reset", "checkout", "switch", "rebase"}

// binary is the executable name.
const binary = "codex"

// Harness is OpenAI Codex.
type Harness struct{}

// Name implements harness.Harness.
func (Harness) Name() harness.Name { return harness.Codex }

// Binary implements harness.Harness.
func (Harness) Binary() string { return binary }

// ImplementScript implements harness.Harness. The container is the sandbox, as
// with claude's --dangerously-skip-permissions. Codex has no system-prompt flag,
// so the rules go ahead of the prompt, and the git guard stands in for
// claude's --disallowedTools.
func (Harness) ImplementScript(in harness.ImplementInput) string {
	args := []string{binary, "exec", "--json", "--dangerously-bypass-approvals-and-sandbox", "--skip-git-repo-check", "-C", in.RepoPath}
	if in.Model != "" {
		args = append(args, "-m", in.Model)
	}
	if in.Resume && in.SessionID != "" {
		args = append(args, "resume", in.SessionID)
	}
	args = append(args, "-")
	prompt := in.Prompt
	if in.SystemPrompt != "" {
		prompt = "## Rules\n\n" + in.SystemPrompt + "\n\n## Task\n\n" + in.Prompt
	}
	return harness.PipeScript(binary, guardDir+":", in.RepoPath, prompt, args)
}

// ReviewScript implements harness.Harness. Codex takes its schema from a file
// and writes its answer to one, so the script writes the schema out and prints
// the answer on stdout at the end. The event stream goes to stderr, which only
// explains a failure.
//
// The review runs without Codex's sandbox, as the implementer does: on Linux
// the read-only sandbox needs bubblewrap and user namespaces, which containers
// often lack. Read-only is enforced instead by the git guard and by comparing
// `git status --porcelain` and `git diff` before and after; a review that changed the tree
// fails and its answer is dropped.
func (Harness) ReviewScript(in harness.ReviewInput) string {
	args := []string{binary, "exec", "--json", "--dangerously-bypass-approvals-and-sandbox", "--skip-git-repo-check", "-C", in.RepoPath}
	if in.Model != "" {
		args = append(args, "-m", in.Model)
	}
	args = append(args, "--output-schema", `"$schema"`, "-o", `"$out"`, "-")
	cmd := harness.PipeCommand(in.RepoPath, in.Prompt, strings.Join(quoteArgs(args), " "))
	schema := base64.StdEncoding.EncodeToString([]byte(in.Schema))
	// status alone misses a second edit to a file that was already modified, so
	// the diff goes into the comparison too.
	repo := "git -C " + sidecar.ShellEscape(in.RepoPath)
	status := "{ " + repo + " status --porcelain; " + repo + " diff HEAD; } 2>&1 | cksum"
	return harness.ScriptHead(binary, guardDir+":") + fmt.Sprintf(`schema=$(mktemp) && out=$(mktemp) || exit 1
echo %s | base64 -d > "$schema"
before=$(%s)
%s 1>&2
code=$?
after=$(%s)
if [ "$before" != "$after" ]; then
  echo "%s" >&2
  code=1
fi
[ $code -eq 0 ] && cat "$out"
rm -f "$schema" "$out"
exit $code
`, schema, status, cmd, status, reviewChangedTree)
}

// reviewChangedTree is what the review script prints when the reviewer changed
// files, which a review must not do.
const reviewChangedTree = "smelt: the review changed files in the working tree (git status differs from before); a review is read-only, so its answer was dropped"

// quoteArgs shell-escapes args except the two file variables, which the shell
// must expand.
func quoteArgs(args []string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		if a == `"$schema"` || a == `"$out"` {
			out = append(out, a)
			continue
		}
		out = append(out, sidecar.ShellEscape(a))
	}
	return out
}

// ParseReview implements harness.Harness: stdout is the last message, which
// the schema made a JSON object of the review and its findings.
func (Harness) ParseReview(stdout string) (harness.ReviewPayload, error) {
	var msg struct {
		Review   string          `json:"review"`
		Findings json.RawMessage `json:"findings"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &msg); err != nil {
		return harness.ReviewPayload{}, fmt.Errorf("codex gave no structured findings: %w", err)
	}
	return harness.ReviewPayload{Review: msg.Review, Findings: msg.Findings}, nil
}

// Env implements harness.Harness. A turn only ever gets an API key; the
// subscription login is a file that Prepare wrote.
func (Harness) Env(cred harness.Credential, _ string) map[string]string {
	env := map[string]string{"CODEX_HOME": codexHome}
	if cred.EnvVar == EnvAPIKey {
		env[EnvAPIKey] = cred.Value
		env["OPENAI_API_KEY"] = cred.Value
	}
	return env
}

// Prepare implements harness.Harness: it installs the git guard and, for a
// subscription login, writes auth.json from the environment, in a directory
// only the user can read.
func (Harness) Prepare(cred harness.Credential) (string, map[string]string) {
	guard := base64.StdEncoding.EncodeToString([]byte(guardScript()))
	script := fmt.Sprintf(`umask 077
mkdir -p %[1]s %[2]s && chmod 700 %[1]s %[2]s || exit 1
echo %[3]s | base64 -d > %[2]s/git && chmod 755 %[2]s/git || exit 1
`, codexHome, guardDir, guard)
	env := map[string]string{}
	if cred.EnvVar == EnvAuthJSON {
		script += fmt.Sprintf(`printf %%s "$%s" | base64 -d > %s/auth.json && chmod 600 %s/auth.json || exit 1
`, EnvAuthJSON, codexHome, codexHome)
		env[EnvAuthJSON] = cred.Value
	}
	return script, env
}

// guardScript is the git wrapper that refuses the commands that would hide the
// implementer's work from the loop, and logs each attempt.
func guardScript() string {
	return fmt.Sprintf(`#!/bin/sh
# Refuses the git commands that move work out of the working tree.
sub=
skip=
for a in "$@"; do
  if [ -n "$skip" ]; then skip=; continue; fi
  case "$a" in
    -C|-c|--git-dir|--work-tree|--namespace) skip=1 ;;
    -*) ;;
    *) sub=$a; break ;;
  esac
done
case "$sub" in
  %s)
    echo "$sub $*" >> %s/attempts.log
    echo "smelt: git $sub is not allowed here; leave your changes uncommitted in the working tree" >&2
    exit 1 ;;
esac
PATH=${PATH#%s:}
exec git "$@"
`, strings.Join(guardedGit, "|"), guardDir, guardDir)
}

// RunError implements harness.Harness.
func (Harness) RunError(ctx context.Context, timeout time.Duration, code int, execErr error, stdout, stderr string) error {
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		return fmt.Errorf("timed out after %s", timeout)
	case execErr != nil:
		return fmt.Errorf("exec: %w", execErr)
	case code == harness.MissingExitCode:
		return ErrMissing
	case code != 0 && (credentialRejectedRe.MatchString(stdout) || credentialRejectedRe.MatchString(stderr)):
		return ErrCredentialRejected
	case code != 0:
		stderr = strings.TrimSpace(stderr)
		if stderr == "" {
			return fmt.Errorf("codex exited %d", code)
		}
		return fmt.Errorf("codex exited %d: %s", code, harness.Tail(stderr, stderrTail))
	}
	return nil
}

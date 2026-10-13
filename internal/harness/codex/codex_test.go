package codex

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"

	"github.com/smeltdev/smelt/internal/harness"
)

func parse(t *testing.T, file string) harness.Outcome {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "testdata", "real", file))
	assert.NilError(t, err)
	p := Harness{}.NewImplementParser(nil)
	p.Write(b)
	p.Flush()
	return p.Outcome()
}

func TestParserReadsARealPass(t *testing.T) {
	var acts []harness.Activity
	b, err := os.ReadFile("../testdata/real/codex-exec-pass.jsonl")
	assert.NilError(t, err)
	p := Harness{}.NewImplementParser(func(a harness.Activity) { acts = append(acts, a) })
	p.Write(b)
	out := p.Outcome()
	assert.Assert(t, out.Done && !out.Failed)
	assert.Equal(t, out.Turn.Summary, "done")
	assert.Equal(t, out.Turn.SessionID, "00000000-0000-4000-8000-000000000001")
	assert.Equal(t, out.Turn.InputTokens, 26710)
	assert.Equal(t, out.Turn.CachedTokens, 24064)
	assert.Equal(t, out.Turn.OutputTokens, 65)
	assert.Assert(t, out.Turn.RateLimit == nil)
	var tools []string
	for _, a := range acts {
		tools = append(tools, a.Tool)
	}
	assert.DeepEqual(t, tools, []string{"", "Bash", ""})
}

func TestParserReadsARealFailure(t *testing.T) {
	out := parse(t, "codex-exec-fail.jsonl")
	assert.Assert(t, out.Done && out.Failed)
	assert.Equal(t, out.Message, "The 'no-such-model-xyz' model is not supported when using Codex with a ChatGPT account.")
	assert.Assert(t, out.Turn.RateLimit == nil)
}

func TestParserRecognisesAUsageLimit(t *testing.T) {
	p := Harness{}.NewImplementParser(nil)
	p.Write([]byte(`{"type":"turn.failed","error":{"message":"You've hit your usage limit. Try again at 2026-10-11T14:05:00Z."}}` + "\n"))
	out := p.Outcome()
	assert.Assert(t, out.Failed)
	rl := out.Turn.RateLimit
	assert.Assert(t, rl != nil)
	assert.Equal(t, rl.Status, "limited")
	assert.Assert(t, rl.ResetsAt != nil && rl.ResetsAt.Equal(time.Date(2026, 10, 11, 14, 5, 0, 0, time.UTC)))
}

func TestImplementScript(t *testing.T) {
	h := Harness{}
	script := h.ImplementScript(harness.ImplementInput{RepoPath: "/work/my repo", Prompt: "do it", SystemPrompt: "no commits", Model: "gpt-5-codex"})
	assert.Assert(t, strings.Contains(script, `export PATH="/tmp/smelt-guard:$HOME/.local/bin:$PATH"`), script)
	assert.Assert(t, strings.Contains(script, "'codex' 'exec' '--json' '--dangerously-bypass-approvals-and-sandbox' '--skip-git-repo-check' '-C' '/work/my repo' '-m' 'gpt-5-codex' '-'"), script)
	assert.Assert(t, !strings.Contains(script, "resume"), script)

	resumed := h.ImplementScript(harness.ImplementInput{RepoPath: "/r", Prompt: "p", SessionID: "thread-1", Resume: true})
	assert.Assert(t, strings.Contains(resumed, "'resume' 'thread-1' '-'"), resumed)
}

func TestImplementScriptPrependsTheRules(t *testing.T) {
	script := Harness{}.ImplementScript(harness.ImplementInput{RepoPath: "/r", Prompt: "do it", SystemPrompt: "no commits"})
	_, line, _ := strings.Cut(script, "echo ")
	enc, _, _ := strings.Cut(line, " ")
	b, err := base64.StdEncoding.DecodeString(enc)
	assert.NilError(t, err)
	assert.Equal(t, string(b), "## Rules\n\nno commits\n\n## Task\n\ndo it")
}

func TestReviewScript(t *testing.T) {
	script := Harness{}.ReviewScript(harness.ReviewInput{RepoPath: "/r", Prompt: "look", Model: "m", Schema: `{"a":1}`})
	assert.Assert(t, strings.Contains(script, "'--dangerously-bypass-approvals-and-sandbox'"), script)
	assert.Assert(t, !strings.Contains(script, "'--sandbox'"), script)
	assert.Assert(t, strings.Contains(script, "git -C '/r' status --porcelain"), script)
	assert.Assert(t, strings.Contains(script, guardDir+":"), script)
	assert.Assert(t, strings.Contains(script, `'--output-schema' "$schema" '-o' "$out" '-'`), script)
	assert.Assert(t, strings.Contains(script, "'-m' 'm'"), script)
}

func TestParserMapsAMissingCommandHostToAHarnessError(t *testing.T) {
	b, err := os.ReadFile("testdata/real/codex-exec-host-missing.jsonl")
	assert.NilError(t, err)
	p := Harness{}.NewImplementParser(nil)
	p.Write(b)
	out := p.Outcome()
	assert.Assert(t, out.Done)
	assert.ErrorIs(t, out.Err, ErrHostMissing)
	assert.ErrorContains(t, out.Err, "smelt factory image --update-kit")
}

func TestParserFindsAMissingHostInEachPlace(t *testing.T) {
	tests := map[string]string{
		"agent message":   `{"type":"item.completed","item":{"type":"agent_message","text":"/x/codex-code-mode-host is missing"}}`,
		"backquoted path": "{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"`/x/codex-code-mode-host` is missing\"}}",
		"failed command":  `{"type":"item.completed","item":{"type":"command_execution","aggregated_output":"Command Host failed to start","exit_code":1}}`,
		"error event":     `{"type":"error","message":"command host failed to start"}`,
		"turn failed":     `{"type":"turn.failed","error":{"message":"codex-code-mode-host is missing"}}`,
		"error item":      `{"type":"item.completed","item":{"type":"error","message":"codex-code-mode-host is missing"}}`,
	}
	for name, line := range tests {
		t.Run(name, func(t *testing.T) {
			p := Harness{}.NewImplementParser(nil)
			p.Write([]byte(line + "\n"))
			assert.ErrorIs(t, p.Outcome().Err, ErrHostMissing)
		})
	}
}

func TestParserIgnoresThePhraseInASuccessfulCommand(t *testing.T) {
	p := Harness{}.NewImplementParser(nil)
	p.Write([]byte(`{"type":"item.completed","item":{"type":"command_execution","aggregated_output":"codex-code-mode-host is missing","exit_code":0}}` + "\n"))
	assert.NilError(t, p.Outcome().Err)
	out := parse(t, "codex-exec-pass.jsonl")
	assert.NilError(t, out.Err)
}

// reviewHarness runs the generated review script in a git repo, with a fake
// codex on PATH that writes a valid answer and then runs body in the repo.
func runReview(t *testing.T, setup, body string) (code int, stdout, stderr string) {
	t.Helper()
	home := t.TempDir()
	repo := t.TempDir()
	bin := filepath.Join(home, ".local", "bin")
	assert.NilError(t, os.MkdirAll(bin, 0o755))
	fake := `#!/bin/sh
cat >/dev/null
while [ $# -gt 0 ]; do [ "$1" = -o ] && out=$2; shift; done
echo '{"review":"fine","findings":[]}' > "$out"
` + body + "\n"
	assert.NilError(t, os.WriteFile(filepath.Join(bin, "codex"), []byte(fake), 0o755))
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		assert.NilError(t, err, string(out))
	}
	git("init", "-q")
	assert.NilError(t, os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("one\n"), 0o644))
	git("add", ".")
	git("commit", "-q", "-m", "base")
	if setup != "" {
		cmd := exec.Command("sh", "-c", setup)
		cmd.Dir = repo
		out, err := cmd.CombinedOutput()
		assert.NilError(t, err, string(out))
	}

	script := Harness{}.ReviewScript(harness.ReviewInput{RepoPath: repo, Prompt: "look", Schema: `{}`})
	var so, se strings.Builder
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = append(os.Environ(), "HOME="+home)
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), so.String(), se.String()
	}
	assert.NilError(t, err)
	return 0, so.String(), se.String()
}

func TestReviewScriptGuardsTheWorkingTree(t *testing.T) {
	tests := []struct {
		name, setup, body string
		wantFail          bool
	}{
		{name: "tree left alone", body: "true"},
		{name: "tracked file modified", body: "echo two > tracked.txt", wantFail: true},
		{name: "untracked file created", body: "echo x > scratch.txt", wantFail: true},
		{name: "tracked file deleted", body: "rm tracked.txt", wantFail: true},
		{name: "pre-dirty tree, unchanged", setup: "echo dirty > tracked.txt; echo u > untracked.txt", body: "true"},
		{name: "pre-dirty tree, changed further", setup: "echo dirty > tracked.txt", body: "echo more >> tracked.txt", wantFail: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, stdout, stderr := runReview(t, tt.setup, tt.body)
			if tt.wantFail {
				assert.Assert(t, code != 0, "stdout=%s stderr=%s", stdout, stderr)
				assert.Assert(t, strings.Contains(stderr, "review changed files"), stderr)
				assert.Equal(t, strings.TrimSpace(stdout), "")
				return
			}
			assert.Equal(t, code, 0, stderr)
			assert.Equal(t, strings.TrimSpace(stdout), `{"review":"fine","findings":[]}`)
		})
	}
}

func TestParseReview(t *testing.T) {
	p, err := Harness{}.ParseReview(`{"review":"fine","findings":[]}` + "\n")
	assert.NilError(t, err)
	assert.Equal(t, p.Review, "fine")
	_, err = Harness{}.ParseReview("not json")
	assert.ErrorContains(t, err, "no structured findings")
}

// Prepare's script runs on the sidecar; here it runs in a temp dir by pointing
// the fixed paths at it.
func TestPrepareWritesTheAuthFileAndGuard(t *testing.T) {
	cred := harness.Credential{EnvVar: EnvAuthJSON, Value: base64.StdEncoding.EncodeToString([]byte(`{"tokens":{"refresh_token":""}}`))}
	script, env := Harness{}.Prepare(cred)
	assert.Equal(t, env[EnvAuthJSON], cred.Value)
	assert.Assert(t, !strings.Contains(script, cred.Value), "the secret is in the environment, not the script")
	_, turnEnv := Harness{}.Env(cred, ""), Harness{}.Env(cred, "")
	_, leaked := turnEnv[EnvAuthJSON]
	assert.Assert(t, !leaked, "later turns do not get the auth file")

	dir := t.TempDir()
	script = strings.NewReplacer(codexHome, dir+"/codex", guardDir, dir+"/guard").Replace(script)
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = append(os.Environ(), EnvAuthJSON+"="+cred.Value)
	out, err := cmd.CombinedOutput()
	assert.NilError(t, err, string(out))

	info, err := os.Stat(filepath.Join(dir, "codex", "auth.json"))
	assert.NilError(t, err)
	assert.Equal(t, info.Mode().Perm(), os.FileMode(0o600))
	dirInfo, err := os.Stat(filepath.Join(dir, "codex"))
	assert.NilError(t, err)
	assert.Equal(t, dirInfo.Mode().Perm(), os.FileMode(0o700))
}

func TestPrepareForAnAPIKeyWritesNoAuthFile(t *testing.T) {
	script, env := Harness{}.Prepare(harness.Credential{EnvVar: EnvAPIKey, Value: "sk-secret"})
	assert.Equal(t, len(env), 0)
	assert.Assert(t, !strings.Contains(script, "auth.json"), script)
	turnEnv := Harness{}.Env(harness.Credential{EnvVar: EnvAPIKey, Value: "sk-secret"}, "")
	assert.Equal(t, turnEnv[EnvAPIKey], "sk-secret")
}

func TestGitGuard(t *testing.T) {
	dir := t.TempDir()
	guard := filepath.Join(dir, "guard")
	realBin := filepath.Join(dir, "real")
	assert.NilError(t, os.MkdirAll(guard, 0o755))
	assert.NilError(t, os.MkdirAll(realBin, 0o755))
	script := strings.ReplaceAll(guardScript(), guardDir, guard)
	assert.NilError(t, os.WriteFile(filepath.Join(guard, "git"), []byte(script), 0o755))
	assert.NilError(t, os.WriteFile(filepath.Join(realBin, "git"), []byte("#!/bin/sh\necho real git \"$@\"\n"), 0o755))

	run := func(args ...string) (string, error) {
		cmd := exec.Command(filepath.Join(guard, "git"), args...)
		cmd.Env = []string{"PATH=" + guard + ":" + realBin + ":/usr/bin:/bin"}
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	for _, sub := range guardedGit {
		out, err := run(sub, "-m", "x")
		assert.Assert(t, err != nil, "%s should be refused", sub)
		assert.Assert(t, strings.Contains(out, "not allowed"), out)
	}
	out, err := run("-C", "/repo", "commit")
	assert.Assert(t, err != nil, "a commit behind -C is refused: %s", out)

	out, err = run("status", "--short")
	assert.NilError(t, err, out)
	assert.Equal(t, strings.TrimSpace(out), "real git status --short")

	log, err := os.ReadFile(filepath.Join(guard, "attempts.log"))
	assert.NilError(t, err)
	assert.Equal(t, strings.Count(string(log), "\n"), len(guardedGit)+1)
}

func TestRunError(t *testing.T) {
	h := Harness{}
	ctx := context.Background()
	assert.NilError(t, h.RunError(ctx, time.Minute, 0, nil, "", ""))
	assert.ErrorIs(t, h.RunError(ctx, time.Minute, harness.MissingExitCode, nil, "", ""), ErrMissing)
	assert.ErrorIs(t, h.RunError(ctx, time.Minute, 1, nil, "", "401 Unauthorized"), ErrCredentialRejected)
	assert.ErrorContains(t, h.RunError(ctx, time.Minute, 2, nil, "", "boom"), "codex exited 2: boom")
}

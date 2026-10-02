package factory

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/review"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
)

// localExec runs scripts with sh on this machine, as a sidecar would, with
// HOME pointed at home so a fake claude can be installed where the scripts
// look for it.
func localExec(home string) review.Execer {
	return func(ctx context.Context, _ *sidecar.PoolEntry, script string, env map[string]string, onOutput circleci.OutputFn, onSubmitted func(string)) (int, error) {
		if onSubmitted != nil {
			onSubmitted("local-command")
		}
		cmd := exec.CommandContext(ctx, "sh", "-c", script)
		cmd.Env = append(os.Environ(), "HOME="+home)
		for k, v := range env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		// The exec API streams one chunk at a time; serialise so the callback
		// sees the same.
		var mu sync.Mutex
		emit := func(stream string) writerFn {
			return func(b []byte) {
				mu.Lock()
				defer mu.Unlock()
				onOutput(stream, b)
			}
		}
		cmd.Stdout = emit(circleci.StreamStdout)
		cmd.Stderr = emit(circleci.StreamStderr)
		// A killed sh can leave a child holding the output pipes open.
		cmd.WaitDelay = 100 * time.Millisecond
		err := cmd.Run()
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode(), nil
		}
		return 0, err
	}
}

type writerFn func([]byte)

func (w writerFn) Write(b []byte) (int, error) {
	w(append([]byte(nil), b...))
	return len(b), nil
}

// installFakeClaude puts a claude on the scripts' PATH that records its
// arguments and stdin, then runs body.
func installFakeClaude(t *testing.T, home, body string) {
	t.Helper()
	bin := filepath.Join(home, ".local", "bin")
	assert.NilError(t, os.MkdirAll(bin, 0o755))
	script := "#!/bin/sh\nprintf '%s\\n--end--\\n' \"$*\" >> \"$HOME/args\"\ncat >> \"$HOME/stdin\"\n" + body + "\n"
	assert.NilError(t, os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0o755))
}

const streamOK = `printf '%s\n' '{"type":"system","subtype":"init","session_id":"s"}' \
 '{"type":"assistant","message":{"content":[{"type":"text","text":"Adding the flag."},{"type":"tool_use","name":"Edit","input":{"file_path":"main.go"}}]}}' \
 '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"go test ./..."}}]}}' \
 '{"type":"result","subtype":"success","is_error":false,"result":"Added --verbose.","total_cost_usd":0.42}'`

func newImplementer(t *testing.T, claudeBody string) (*Implementer, string, *[]Activity) {
	t.Helper()
	home := t.TempDir()
	installFakeClaude(t, home, claudeBody)
	var acts []Activity
	im := &Implementer{
		Exec:       localExec(home),
		Entry:      &sidecar.PoolEntry{ID: "impl", RepoPath: t.TempDir()},
		Credential: review.Credential{EnvVar: "ANTHROPIC_API_KEY", Value: "k"},
		OnActivity: func(a Activity) { acts = append(acts, a) },
	}
	return im, home, &acts
}

func TestImplementerRunsAndReportsActivity(t *testing.T) {
	im, home, acts := newImplementer(t, streamOK)

	turn, err := im.Run(context.Background(), "add a --verbose flag")
	assert.NilError(t, err)
	assert.Equal(t, turn.Summary, "Added --verbose.")
	assert.Equal(t, turn.CostUSD, 0.42)
	assert.DeepEqual(t, *acts, []Activity{
		{Detail: "Adding the flag."},
		{Tool: "Edit", Detail: "main.go"},
		{Tool: "Bash", Detail: "go test ./..."},
	})

	stdin, err := os.ReadFile(filepath.Join(home, "stdin"))
	assert.NilError(t, err)
	assert.Equal(t, string(stdin), "add a --verbose flag")
}

// TestImplementerResumesItsSession guards the feedback rounds: each must
// continue the first turn's conversation, not start a fresh one without the
// context of the work it is fixing.
func TestImplementerResumesItsSession(t *testing.T) {
	im, home, _ := newImplementer(t, streamOK)

	_, err := im.Run(context.Background(), "first")
	assert.NilError(t, err)
	_, err = im.Run(context.Background(), "fix it")
	assert.NilError(t, err)

	args, err := os.ReadFile(filepath.Join(home, "args"))
	assert.NilError(t, err)
	lines := strings.Split(strings.TrimSuffix(string(args), "--end--\n"), "--end--\n")
	assert.Equal(t, len(lines), 2)
	assert.Assert(t, strings.Contains(lines[0], "--session-id "+im.sessionID), lines[0])
	assert.Assert(t, strings.Contains(lines[1], "--resume "+im.sessionID), lines[1])
	assert.Assert(t, strings.Contains(lines[0], "Bash(git commit:*)"), "git commits are disallowed: %s", lines[0])
}

func TestImplementerFailures(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
		is   error
	}{
		{
			name: "error result",
			body: `echo '{"type":"result","is_error":true,"result":"API overloaded"}'; exit 1`,
			want: "implementer: claude exited 1: API overloaded",
		},
		{
			name: "credential rejected",
			body: `echo '{"type":"result","is_error":true,"result":"Failed to authenticate. API Error: 401"}'; exit 1`,
			is:   review.ErrCredentialRejected,
		},
		{
			name: "credential rejected on stderr",
			body: `echo '{"type":"result","is_error":true,"result":"Something went wrong"}'; echo 'Failed to authenticate. API Error: 401' >&2; exit 1`,
			is:   review.ErrCredentialRejected,
		},
		{
			name: "error result without text",
			body: `echo '{"type":"result","is_error":true,"result":""}'; echo boom >&2`,
			want: "implementer: boom",
		},
		{
			name: "no result",
			body: `echo '{"type":"system","session_id":"s"}'`,
			want: "without a result",
		},
		{
			name: "stderr only",
			body: `echo boom >&2; exit 2`,
			want: "implementer: claude exited 2: boom",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			im, _, _ := newImplementer(t, tc.body)
			_, err := im.Run(context.Background(), "p")
			if tc.is != nil {
				assert.Assert(t, errors.Is(err, tc.is), "got %v", err)
				return
			}
			assert.ErrorContains(t, err, tc.want)
		})
	}
}

func TestImplementerReportsMissingClaude(t *testing.T) {
	home := t.TempDir()
	im := &Implementer{Exec: localExec(home), Entry: &sidecar.PoolEntry{RepoPath: t.TempDir()}}
	t.Setenv("PATH", "/usr/bin:/bin")
	_, err := im.Run(context.Background(), "p")
	assert.Assert(t, errors.Is(err, review.ErrClaudeMissing), "got %v", err)
}

// TestStreamParserReassemblesSplitLines covers output arriving in chunks that
// cut lines anywhere, as the exec stream delivers it.
func TestStreamParserReassemblesSplitLines(t *testing.T) {
	var acts []Activity
	p := &streamParser{onActivity: func(a Activity) { acts = append(acts, a) }}
	stream := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"a.go"}}]}}` + "\n" +
		`{"type":"result","result":"ok"}`
	for i := 0; i < len(stream); i += 7 {
		p.write([]byte(stream[i:min(i+7, len(stream))]))
	}
	p.flush()
	assert.DeepEqual(t, acts, []Activity{{Tool: "Read", Detail: "a.go"}})
	assert.Assert(t, p.sawResult)
	assert.Equal(t, p.result.Result, "ok")
}

func TestStreamParserDropsOversizedLines(t *testing.T) {
	p := &streamParser{}
	p.write([]byte(`{"type":"user","x":"` + strings.Repeat("a", maxLineBytes) + `"}` + "\n"))
	p.write([]byte(`{"type":"result","result":"ok"}` + "\n"))
	assert.Assert(t, p.sawResult)
	assert.Assert(t, len(p.buf) < maxLineBytes)
}

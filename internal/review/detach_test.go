package review

import (
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"
)

func TestDetachScript(t *testing.T) {
	script := DetachScript(DetachSpec{
		RunID:       "20260929-120000",
		RepoPath:    "/home/user/my repo",
		OrgID:       "org-1",
		Parallelism: 3,
		Model:       "opus",
		Timeout:     20 * time.Minute,
		PromptsDir:  "reviews/$(evil)",
		Install:     "echo installing",
	})

	// Installation happens before the review starts, and the review is
	// detached from the exec that starts it.
	assert.Assert(t, strings.Index(script, "echo installing") < strings.Index(script, "nohup setsid"))
	assert.Assert(t, strings.Contains(script, "cd '/home/user/my repo'"), script)
	assert.Assert(t, strings.Contains(script, "</dev/null &"), script)

	// Caller-supplied values cannot reach the shell unquoted.
	// The directory is quoted once for the inner shell, then the whole inner
	// command is quoted again for the outer one.
	assert.Assert(t, strings.Contains(script, `'\''reviews/$(evil)'\''`), script)
	assert.Assert(t, strings.Contains(script, "--parallelism 3"), script)
	assert.Assert(t, strings.Contains(script, "--timeout"), script)

	// A chunk without 'review' is caught before anything is backgrounded.
	assert.Assert(t, strings.Index(script, "review --help") < strings.Index(script, "nohup setsid"), script)
	assert.Assert(t, strings.Contains(script, "|| exit 64"), script)

	// The run directory is the last thing printed, for the caller to read.
	assert.Assert(t, strings.HasSuffix(script, "echo \"$RUN\"\n"), script)
	assert.Assert(t, strings.Contains(script, `RUN="$HOME/.chunk-review/20260929-120000"`), script)
}

func TestDetachScriptOmitsUnsetOptions(t *testing.T) {
	script := DetachScript(DetachSpec{RunID: "r", RepoPath: "/r", OrgID: "o", Parallelism: 1})
	for _, unwanted := range []string{"--model", "--timeout"} {
		assert.Assert(t, !strings.Contains(script, unwanted), "%s in %s", unwanted, script)
	}
}

func TestDetachEnv(t *testing.T) {
	env := DetachEnv("circle-token", Options{
		Credential: Credential{EnvVar: "CLAUDE_CODE_OAUTH_TOKEN", Value: "claude-token"},
	})
	assert.DeepEqual(t, env, map[string]string{
		"CIRCLECI_TOKEN":          "circle-token",
		"CLAUDE_CODE_OAUTH_TOKEN": "claude-token",
	})
}

func TestParseRead(t *testing.T) {
	tests := []struct {
		name    string
		out     string
		want    RunStatus
		wantErr bool
	}{
		{name: "done", out: "STATUS done 0\n{\"failed\":0}\n", want: RunStatus{State: RunDone, ExitCode: 0, Body: "{\"failed\":0}\n"}},
		{name: "done with log", out: "STATUS done 1\n\nSTATUS-LOG\nboom\n", want: RunStatus{State: RunDone, ExitCode: 1, Body: "", Log: "boom\n"}},
		{name: "done, failed", out: "STATUS done 1\n{}", want: RunStatus{State: RunDone, ExitCode: 1, Body: "{}"}},
		{name: "running", out: "STATUS running\n  reviewing a\n", want: RunStatus{State: RunRunning, Body: "  reviewing a\n"}},
		{name: "missing", out: "STATUS missing\n", want: RunStatus{State: RunMissing}},
		{name: "no status line", out: "hello\n", wantErr: true},
		{name: "done without code", out: "STATUS done\n", wantErr: true},
		{name: "unknown state", out: "STATUS lost\n", wantErr: true},
		{name: "empty", out: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseRead(tt.out)
			if tt.wantErr {
				assert.Assert(t, err != nil)
				return
			}
			assert.NilError(t, err)
			assert.DeepEqual(t, got, tt.want)
		})
	}
}

func TestReadScriptQuotesRunDir(t *testing.T) {
	script := ReadScript("/home/user/.chunk-review/a b'c")
	assert.Assert(t, strings.HasPrefix(script, `cd '/home/user/.chunk-review/a b'\''c'`), script)
}

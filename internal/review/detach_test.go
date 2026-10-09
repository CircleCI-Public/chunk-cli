package review

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/harness/claudecode"
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
	assert.Assert(t, strings.Contains(script, "exit 66;; *) exit 64"), script)

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
		Credential: claudecode.Credential{EnvVar: "CLAUDE_CODE_OAUTH_TOKEN", Value: "claude-token"},
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
		{name: "died", out: "STATUS died\n  boom\n", want: RunStatus{State: RunDied, Body: "  boom\n"}},
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

func TestDetachScriptPassesReviewOptions(t *testing.T) {
	script := DetachScript(DetachSpec{RunID: "r", RepoPath: "/r", OrgID: "o", Parallelism: 1, Image: "snap-1", DestroyPool: true})
	assert.Assert(t, strings.Contains(script, "--image"), script)
	assert.Assert(t, strings.Contains(script, "snap-1"), script)
	assert.Assert(t, strings.Contains(script, "--destroy-pool"), script)

	plain := DetachScript(DetachSpec{RunID: "r", RepoPath: "/r", OrgID: "o", Parallelism: 1})
	for _, unwanted := range []string{"--image", "--destroy-pool"} {
		assert.Assert(t, !strings.Contains(plain, unwanted), "%s in %s", unwanted, plain)
	}
}

func TestUploadInstallReplacesTheBinaryInPlace(t *testing.T) {
	// Writing over a running chunk fails; a new file moved over it does not.
	assert.Assert(t, strings.Contains(UploadInstall, `mv -f "$HOME/chunk.new" "$HOME/chunk"`), UploadInstall)
	assert.Assert(t, !strings.Contains(UploadInstall, `> "$HOME/chunk" `), UploadInstall)
}

// runDetachScript runs the detach script in a shell whose home holds the given
// stand-in for chunk, and returns the script's exit code.
func runDetachScript(t *testing.T, chunk string) int {
	t.Helper()
	home := t.TempDir()
	if chunk != "" {
		assert.NilError(t, os.WriteFile(filepath.Join(home, "chunk"), []byte(chunk), 0o755))
	}
	script := DetachScript(DetachSpec{RunID: "r", RepoPath: t.TempDir(), OrgID: "o", Parallelism: 1})
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}
	err := cmd.Run()
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	assert.Assert(t, errors.As(err, &exitErr), "running the script: %v", err)
	return exitErr.ExitCode()
}

func TestDetachScriptTellsBrokenChunkFromOneWithoutReview(t *testing.T) {
	t.Run("not executable as a program", func(t *testing.T) {
		// A file the kernel cannot run, such as a build for another OS.
		assert.Equal(t, runDetachScript(t, "garbage"), ExitBadBinary)
	})
	t.Run("missing", func(t *testing.T) {
		assert.Equal(t, runDetachScript(t, ""), ExitBadBinary)
	})
	t.Run("runs but has no review command", func(t *testing.T) {
		assert.Equal(t, runDetachScript(t, "#!/bin/sh\nexit 1\n"), ExitNoReview)
	})
}

// writeRun makes a run directory with the given files and returns its path.
func writeRun(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "run with space")
	assert.NilError(t, os.MkdirAll(dir, 0o755))
	for name, content := range files {
		assert.NilError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
	}
	return dir
}

func readRun(t *testing.T, dir string) RunStatus {
	t.Helper()
	out, err := exec.Command("sh", "-c", ReadScript(dir)).Output()
	assert.NilError(t, err)
	st, err := ParseRead(string(out))
	assert.NilError(t, err)
	return st
}

func TestReadScriptStates(t *testing.T) {
	t.Run("done keeps the exit code", func(t *testing.T) {
		st := readRun(t, writeRun(t, map[string]string{ExitFile: "3\n", ResultFile: `{"failed":0}`, LogFile: "oops\n"}))
		assert.Equal(t, st.State, RunDone)
		assert.Equal(t, st.ExitCode, 3)
		assert.Equal(t, strings.TrimSpace(st.Body), `{"failed":0}`)
		assert.Equal(t, st.Log, "oops\n")
	})

	t.Run("running while its process is alive", func(t *testing.T) {
		st := readRun(t, writeRun(t, map[string]string{PidFile: strconv.Itoa(os.Getpid()), LogFile: "working\n"}))
		assert.Equal(t, st.State, RunRunning)
	})

	t.Run("running before the pid is written", func(t *testing.T) {
		st := readRun(t, writeRun(t, nil))
		assert.Equal(t, st.State, RunRunning)
	})

	t.Run("died when its process is gone and there is no exit code", func(t *testing.T) {
		gone := exec.Command("true")
		assert.NilError(t, gone.Run())
		st := readRun(t, writeRun(t, map[string]string{PidFile: strconv.Itoa(gone.Process.Pid), LogFile: "last words\n"}))
		assert.Equal(t, st.State, RunDied)
		assert.Equal(t, st.Body, "last words\n")
	})

	t.Run("missing", func(t *testing.T) {
		st := readRun(t, filepath.Join(t.TempDir(), "nope"))
		assert.Equal(t, st.State, RunMissing)
	})
}

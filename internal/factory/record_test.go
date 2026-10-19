package factory

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

// runHostRecordScript runs HostRecordScript for real, under HOME=home, the
// way a pull execs it on the host sidecar, and returns what it printed.
func runHostRecordScript(t *testing.T, home string) (stdout, stderr string, exitCode int) {
	t.Helper()
	cmd := exec.Command("sh", "-c", HostRecordScript())
	cmd.Env = append(os.Environ(), "HOME="+home)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		} else {
			t.Fatalf("run host record script: %v", err)
		}
	}
	return outBuf.String(), errBuf.String(), code
}

// writeHostFactoryFile writes name under the factory data directory a
// per-run host keeps its own records in, mirroring where SaveRecord and
// PoolStatePath put them.
func writeHostFactoryFile(t *testing.T, home, name, content string) {
	t.Helper()
	dir := filepath.Join(home, ".local", "share", "chunk", "repo-abc123", "factory")
	assert.NilError(t, os.MkdirAll(dir, 0o755))
	assert.NilError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
}

// The script must find the one run record on a per-run host and print it
// exactly, ignoring the pool's own state file beside it - dropping that
// exclusion would otherwise count two files here and refuse to print either.
func TestHostRecordScriptPrintsTheSingleNonPoolRecord(t *testing.T) {
	home := t.TempDir()
	writeHostFactoryFile(t, home, "r-abc123.json", `{"run_id":"r-abc123","worktree":"/home/user/repo"}`)
	writeHostFactoryFile(t, home, "r-abc123.pool.json", `{"ignored":"pool state"}`)

	stdout, stderr, code := runHostRecordScript(t, home)

	assert.Equal(t, code, 0, "stderr: %s", stderr)
	assert.Equal(t, stdout, `{"run_id":"r-abc123","worktree":"/home/user/repo"}`)
}

// With no run record at all, the script must fail rather than print nothing:
// a pull reading empty output as a worktree would otherwise rsync against "".
func TestHostRecordScriptFailsWithNoRecord(t *testing.T) {
	home := t.TempDir()
	assert.NilError(t, os.MkdirAll(filepath.Join(home, ".local", "share", "chunk"), 0o755))

	stdout, stderr, code := runHostRecordScript(t, home)

	assert.Assert(t, code != 0, "stdout: %s", stdout)
	assert.Assert(t, strings.Contains(stderr, "found 0"), "stderr: %s", stderr)
}

// Two run records on the same host should never happen on a per-run box, but
// the script must refuse rather than guess which worktree is the right one
// (or, worse, cat both and hand back something that parses as neither).
func TestHostRecordScriptFailsWithMoreThanOneRecord(t *testing.T) {
	home := t.TempDir()
	writeHostFactoryFile(t, home, "r-one.json", `{"run_id":"r-one"}`)
	writeHostFactoryFile(t, home, "r-two.json", `{"run_id":"r-two"}`)

	stdout, stderr, code := runHostRecordScript(t, home)

	assert.Assert(t, code != 0, "stdout: %s", stdout)
	assert.Assert(t, strings.Contains(stderr, "found 2"), "stderr: %s", stderr)
}

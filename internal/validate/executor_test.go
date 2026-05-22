package validate

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
)

func newTestStreams() (iostream.Streams, *bytes.Buffer, *bytes.Buffer) {
	var out, errBuf bytes.Buffer
	return iostream.Streams{Out: &out, Err: &errBuf}, &out, &errBuf
}

// --- ShellEscape ---

func TestShellEscape(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"plain path", "/workspace/src", "'/workspace/src'"},
		{"single quote", "it's", "'it'\\''s'"},
		{"multiple single quotes", "a'b'c", "'a'\\''b'\\''c'"},
		{"dollar sign", "$HOME", "'$HOME'"},
		{"newline", "foo\nbar", "'foo\nbar'"},
		{"backtick", "`cmd`", "'`cmd`'"},
		{"backslash", `foo\bar`, "'foo\\bar'"},
		{"spaces", "hello world", "'hello world'"},
		{"empty string", "", "''"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shellEscape(tt.input)
			assert.Equal(t, got, tt.want)
		})
	}
}

// --- LocalExecutor ---

func TestLocalExecutor_Success(t *testing.T) {
	streams, out, _ := newTestStreams()
	exec := NewLocalExecutor(".", streams)

	err := exec.Execute(context.Background(), "test", "echo hello", 0)
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(out.String(), "hello"), "got: %s", out.String())
}

func TestLocalExecutor_Failure(t *testing.T) {
	streams, _, _ := newTestStreams()
	exec := NewLocalExecutor(".", streams)

	err := exec.Execute(context.Background(), "test", "false", 0)
	assert.Assert(t, err != nil)
	assert.Assert(t, strings.Contains(err.Error(), "test command failed"), "got: %v", err)
}

func TestLocalExecutor_Timeout(t *testing.T) {
	streams, _, _ := newTestStreams()
	exec := NewLocalExecutor(".", streams)

	err := exec.Execute(context.Background(), "test", "sleep 10", 1)
	assert.Assert(t, err != nil)
	assert.Assert(t, strings.Contains(err.Error(), "timed out"), "got: %v", err)
}

func TestLocalExecutor_ExpandChangedPackages(t *testing.T) {
	// Run in a temp directory (not a git repo) so git diff fails and
	// expandCommand falls back to "./...".
	dir := t.TempDir()
	streams, out, _ := newTestStreams()
	exec := NewLocalExecutor(dir, streams)

	err := exec.Execute(context.Background(), "test", "echo {{CHANGED_PACKAGES}}", 0)
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(out.String(), "./..."), "got: %s", out.String())
}

// --- RemoteExecutor ---

func TestRemoteExecutor_Success(t *testing.T) {
	var capturedScript string
	execFn := func(_ context.Context, script string) (string, string, int, error) {
		capturedScript = script
		return "remote output\n", "", 0, nil
	}
	streams, out, _ := newTestStreams()
	exec := NewRemoteExecutor(execFn, "/workspace/repo", streams)

	err := exec.Execute(context.Background(), "test", "echo hello", 0)
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(out.String(), "remote output"), "got: %s", out.String())
	assert.Assert(t, strings.HasPrefix(capturedScript, "cd '/workspace/repo' &&"), "got: %s", capturedScript)
}

func TestRemoteExecutor_NonZeroExitCode(t *testing.T) {
	execFn := func(_ context.Context, _ string) (string, string, int, error) {
		return "", "", 1, nil
	}
	streams, _, _ := newTestStreams()
	exec := NewRemoteExecutor(execFn, "/workspace", streams)

	err := exec.Execute(context.Background(), "test", "failing", 0)
	assert.Assert(t, err != nil)
	assert.Assert(t, strings.Contains(err.Error(), "remote test failed"), "got: %v", err)
}

func TestRemoteExecutor_ExecError(t *testing.T) {
	execFn := func(_ context.Context, _ string) (string, string, int, error) {
		return "", "", 0, fmt.Errorf("connection lost")
	}
	streams, _, _ := newTestStreams()
	exec := NewRemoteExecutor(execFn, "/workspace", streams)

	err := exec.Execute(context.Background(), "test", "echo hi", 0)
	assert.Assert(t, err != nil)
	assert.Assert(t, strings.Contains(err.Error(), "remote test"), "got: %v", err)
	assert.Assert(t, strings.Contains(err.Error(), "connection lost"), "got: %v", err)
}

func TestRemoteExecutor_EmptyStdoutNotWritten(t *testing.T) {
	execFn := func(_ context.Context, _ string) (string, string, int, error) {
		return "", "", 0, nil
	}
	streams, out, _ := newTestStreams()
	exec := NewRemoteExecutor(execFn, "/workspace", streams)

	err := exec.Execute(context.Background(), "test", "silent", 0)
	assert.NilError(t, err)
	assert.Equal(t, out.Len(), 0)
}

func TestRemoteExecutor_WorkspaceExists(t *testing.T) {
	t.Run("exists", func(t *testing.T) {
		execFn := func(_ context.Context, script string) (string, string, int, error) {
			assert.Assert(t, strings.Contains(script, "test -d"))
			assert.Assert(t, strings.Contains(script, "/workspace"))
			return "", "", 0, nil
		}
		exec := NewRemoteExecutor(execFn, "/workspace", iostream.Streams{})
		assert.NilError(t, exec.WorkspaceExists(context.Background()))
	})

	t.Run("missing", func(t *testing.T) {
		execFn := func(_ context.Context, _ string) (string, string, int, error) {
			return "", "", 1, nil
		}
		exec := NewRemoteExecutor(execFn, "/missing", iostream.Streams{})
		assert.ErrorIs(t, exec.WorkspaceExists(context.Background()), ErrWorkspaceNotFound)
	})

	t.Run("exec error", func(t *testing.T) {
		execFn := func(_ context.Context, _ string) (string, string, int, error) {
			return "", "", 0, fmt.Errorf("ssh failed")
		}
		exec := NewRemoteExecutor(execFn, "/workspace", iostream.Streams{})
		assert.ErrorContains(t, exec.WorkspaceExists(context.Background()), "ssh failed")
	})
}

func TestRemoteExecutor_Dest(t *testing.T) {
	exec := NewRemoteExecutor(nil, "/some/path", iostream.Streams{})
	assert.Equal(t, exec.Dest(), "/some/path")
}

func TestRemoteExecutor_WriteTo(t *testing.T) {
	execFn := func(_ context.Context, _ string) (string, string, int, error) {
		return "stdout\n", "stderr\n", 0, nil
	}
	var outBuf, errBuf bytes.Buffer
	original := NewRemoteExecutor(execFn, "/workspace", iostream.Streams{})
	reExec := original.WriteTo(&outBuf, &errBuf)

	err := reExec.Execute(context.Background(), "test", "cmd", 0)
	assert.NilError(t, err)
	assert.Equal(t, outBuf.String(), "stdout\n")
	assert.Equal(t, errBuf.String(), "stderr\n")
}

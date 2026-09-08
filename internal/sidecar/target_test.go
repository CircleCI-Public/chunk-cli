package sidecar_test

import (
	"bytes"
	"context"
	"net/http/httptest"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
)

func TestTargetExecRunnerReturnsAndStreamsOutput(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.ExecResponse = &fakes.ExecResponse{
		CommandID: "cmd-1",
		Stdout:    "output\n",
		Stderr:    "warning\n",
		ExitCode:  3,
	}
	srv := httptest.NewServer(cci)
	defer srv.Close()

	var out, errOut bytes.Buffer
	target := sidecar.Target{
		Client:    newClient(t, srv.URL),
		SidecarID: "sb-1",
		Workdir:   "/workspace/repo",
	}
	runner, dest, err := target.ExecRunner(context.Background(), t.TempDir(), nil, iostream.Streams{Out: &out, Err: &errOut})
	assert.NilError(t, err)
	assert.Equal(t, dest, "/workspace/repo")

	stdout, stderr, exitCode, err := runner(context.Background(), "exit 3")
	assert.NilError(t, err)
	assert.Equal(t, stdout, "output\n")
	assert.Equal(t, stderr, "warning\n")
	assert.Equal(t, exitCode, 3)
	assert.Equal(t, out.String(), stdout)
	assert.Equal(t, errOut.String(), stderr)

	stdout, stderr, exitCode, err = runner(context.Background(), "exit 3")
	assert.NilError(t, err)
	assert.Equal(t, stdout, "output\n")
	assert.Equal(t, stderr, "warning\n")
	assert.Equal(t, exitCode, 3)
}

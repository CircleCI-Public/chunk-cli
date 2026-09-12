package cmd

import (
	"context"
	"io"
	"net/http/httptest"
	"testing"
	"time"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/eventlog"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
)

func TestPooledValidateRegistersSubmittedCommand(t *testing.T) {
	regs := captureRegistrations(t)
	cci := fakes.NewFakeCircleCI()
	cci.ExecResponse = &fakes.ExecResponse{CommandID: "cmd-1"}
	srv := httptest.NewServer(cci)
	t.Cleanup(srv.Close)

	client, err := circleci.NewClient(circleci.Config{Token: "test-token", BaseURL: srv.URL})
	assert.NilError(t, err)
	projectRoot := t.TempDir()
	var recordedID string
	result := runPooledValidateCommand(
		context.Background(),
		&sidecar.PoolEntry{ID: "sb-1", RepoPath: "/workspace/repo", Client: client},
		config.Command{Name: "test", Run: "true"},
		"", projectRoot, "", nil,
		func(id string) { recordedID = id },
		func(iostream.Level, string) {},
		iostream.Streams{Out: io.Discard, Err: io.Discard},
	)
	assert.NilError(t, result.Err)
	assert.Equal(t, recordedID, "cmd-1")

	select {
	case reg := <-regs:
		assert.Equal(t, reg.CommandID, "cmd-1")
		assert.Equal(t, reg.SidecarID, "sb-1")
		assert.Equal(t, reg.ProjectRoot, projectRoot)
		assert.Equal(t, reg.Op, string(eventlog.OpValidate))
		assert.Equal(t, reg.Name, "test")
		assert.Assert(t, !reg.SubmittedAt.IsZero())
	case <-time.After(5 * time.Second):
		t.Fatal("pooled validation command was not registered")
	}
}

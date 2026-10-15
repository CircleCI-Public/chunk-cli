package cmd

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/review"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
)

func TestRelativePromptsDir(t *testing.T) {
	work := t.TempDir()

	tests := []struct {
		name    string
		dir     string
		want    string
		wantErr bool
	}{
		{name: "default", dir: "", want: ""},
		{name: "relative", dir: "prompts/a", want: "prompts/a"},
		{name: "absolute inside", dir: work + "/prompts", want: "prompts"},
		{name: "outside", dir: "../elsewhere", wantErr: true},
		{name: "absolute outside", dir: "/etc", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := relativePromptsDir(work, tt.dir)
			if tt.wantErr {
				assert.Assert(t, err != nil)
				return
			}
			assert.NilError(t, err)
			assert.Equal(t, got, tt.want)
		})
	}
}

func TestDetachedStateRoundTrip(t *testing.T) {
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
	work := t.TempDir()

	_, err := loadDetachedState(work)
	assert.Assert(t, errors.Is(err, os.ErrNotExist), "got %v", err)

	want := detachedState{SidecarID: "sc-1", RunDir: "/home/user/.chunk-review/r", StartedAt: time.Unix(1_700_000_000, 0).UTC()}
	assert.NilError(t, saveDetachedState(work, want))

	got, err := loadDetachedState(work)
	assert.NilError(t, err)
	assert.Equal(t, got.SidecarID, want.SidecarID)
	assert.Equal(t, got.RunDir, want.RunDir)
	assert.Assert(t, got.StartedAt.Equal(want.StartedAt))

	path, err := detachedStatePath(work)
	assert.NilError(t, err)
	info, err := os.Stat(path)
	assert.NilError(t, err)
	assert.Equal(t, info.Mode().Perm(), os.FileMode(0o600))
	_, err = os.Stat(filepath.Join(work, ".chunk"))
	assert.Assert(t, errors.Is(err, os.ErrNotExist), "expected nothing written in the repo, got %v", err)
}

func TestDetachedStateReadsLegacyPath(t *testing.T) {
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
	work := t.TempDir()
	legacy := filepath.Join(work, ".chunk", "review-run.json")
	assert.NilError(t, os.MkdirAll(filepath.Dir(legacy), 0o700))
	assert.NilError(t, os.WriteFile(legacy, []byte(`{"sidecar_id":"sc-old"}`), 0o600))

	got, err := loadDetachedState(work)
	assert.NilError(t, err)
	assert.Equal(t, got.SidecarID, "sc-old")

	// The new path wins once written, and the legacy file goes.
	assert.NilError(t, saveDetachedState(work, detachedState{SidecarID: "sc-new"}))
	got, err = loadDetachedState(work)
	assert.NilError(t, err)
	assert.Equal(t, got.SidecarID, "sc-new")
	_, err = os.Stat(legacy)
	assert.Assert(t, errors.Is(err, os.ErrNotExist), "expected legacy record removed, got %v", err)
}

func TestLastLine(t *testing.T) {
	assert.Equal(t, lastLine("/home/u/.chunk-review/r\n"), "/home/u/.chunk-review/r")
	assert.Equal(t, lastLine("installing...\ndone\n/home/my user/.chunk-review/r\n"), "/home/my user/.chunk-review/r")
	assert.Equal(t, lastLine("\n\n"), "")
	assert.Equal(t, lastLine(""), "")
}

func TestDetachedFailure(t *testing.T) {
	clean := reviewReport{Reviews: make([]reviewJSON, 2)}

	assert.NilError(t, detachedFailure(clean, review.RunStatus{State: review.RunDone}))

	// A report with no failures does not hide a run that exited nonzero.
	err := detachedFailure(clean, review.RunStatus{State: review.RunDone, ExitCode: 3, Log: "stopped early\n"})
	var ue *userError
	assert.Assert(t, errors.As(err, &ue), "got %v", err)
	assert.Assert(t, strings.Contains(ue.msg, "exited with code 3"), ue.msg)
	assert.Assert(t, strings.Contains(ue.suggestion, "stopped early"), ue.suggestion)

	failed := reviewReport{Failed: 1, Reviews: make([]reviewJSON, 2)}
	err = detachedFailure(failed, review.RunStatus{State: review.RunDone})
	assert.Assert(t, errors.As(err, &ue), "got %v", err)
	assert.Assert(t, strings.Contains(ue.msg, "1 of 2 review(s) failed"), ue.msg)
}

func TestCheckNoActiveRun(t *testing.T) {
	// The previous run is described to the guard by what its primary sidecar
	// prints when asked, so each case fakes that answer.
	tests := []struct {
		name      string
		saved     bool
		stdout    string
		exitCode  int
		statusErr int
		wantBusy  bool
	}{
		{name: "no previous run", saved: false},
		{name: "previous run is still going", saved: true, stdout: "STATUS running\nworking\n", wantBusy: true},
		{name: "previous run finished", saved: true, stdout: "STATUS done 0\n{}\n"},
		{name: "previous run died", saved: true, stdout: "STATUS died\n"},
		{name: "previous run is gone from the sidecar", saved: true, stdout: "STATUS missing\n"},
		{name: "primary sidecar expired", saved: true, statusErr: http.StatusNotFound},
		{name: "primary could not be read", saved: true, stdout: "STATUS running\n", exitCode: 1},
		{name: "unintelligible answer", saved: true, stdout: "hello\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cci := fakes.NewFakeCircleCI()
			cci.ExecResponse = &fakes.ExecResponse{CommandID: "cmd-1", Stdout: tt.stdout, ExitCode: tt.exitCode}
			cci.ExecStatusCode = tt.statusErr
			srv := httptest.NewServer(cci)
			t.Cleanup(srv.Close)
			client, err := circleci.NewClient(circleci.Config{Token: "test-token", BaseURL: srv.URL})
			assert.NilError(t, err)

			t.Setenv(config.EnvXDGDataHome, t.TempDir())
			work := t.TempDir()
			if tt.saved {
				assert.NilError(t, saveDetachedState(work, detachedState{
					SidecarID: "sc-1", RunDir: "/home/user/.chunk-review/r", StartedAt: time.Now().Add(-time.Minute),
				}))
			}

			err = checkNoActiveRun(context.Background(), client, work)
			if !tt.wantBusy {
				assert.NilError(t, err)
				return
			}
			var ue *userError
			assert.Assert(t, errors.As(err, &ue), "got %v", err)
			assert.Assert(t, strings.Contains(ue.msg, "still running on sidecar sc-1"), ue.msg)
			assert.Assert(t, strings.Contains(ue.suggestion, "chunk review results"), ue.suggestion)
		})
	}
}

func TestCheckPromptsSynced(t *testing.T) {
	work := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = work
		out, err := cmd.CombinedOutput()
		assert.NilError(t, err, string(out))
	}
	run("init", "-q")
	assert.NilError(t, os.WriteFile(filepath.Join(work, ".gitignore"), []byte("ignored/\ntracked-ignored/c.md\n"), 0o644))
	for _, p := range []string{"prompts/a.md", "ignored/b.md", "tracked-ignored/c.md"} {
		assert.NilError(t, os.MkdirAll(filepath.Join(work, filepath.Dir(p)), 0o755))
		assert.NilError(t, os.WriteFile(filepath.Join(work, p), []byte("review"), 0o644))
	}
	run("add", "-f", "tracked-ignored/c.md")

	ctx := context.Background()
	assert.NilError(t, checkPromptsSynced(ctx, work, filepath.Join(work, "prompts"), 1))
	assert.Assert(t, checkPromptsSynced(ctx, work, filepath.Join(work, "ignored"), 1) != nil)
	assert.Assert(t, checkPromptsSynced(ctx, work, filepath.Join(work, "tracked-ignored"), 1) != nil)
}

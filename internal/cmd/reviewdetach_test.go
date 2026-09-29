package cmd

import (
	"errors"
	"os"
	"testing"
	"time"

	"gotest.tools/v3/assert"
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

	info, err := os.Stat(detachedStatePath(work))
	assert.NilError(t, err)
	assert.Equal(t, info.Mode().Perm(), os.FileMode(0o600))
}

package cmd

import (
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

func TestSnapshotCreateNameTooLong(t *testing.T) {
	cmd := newSidecarSnapshotCreateCmd()
	cmd.SetOut(nil)
	cmd.SetErr(nil)

	longName := strings.Repeat("a", 256)
	cmd.SetArgs([]string{"--name", longName})

	err := cmd.Execute()
	assert.ErrorContains(t, err, "255 characters or fewer")
	assert.ErrorContains(t, err, "256")
}

func TestSnapshotCreateNameAtLimit(t *testing.T) {
	cmd := newSidecarSnapshotCreateCmd()

	exactName := strings.Repeat("a", 255)
	cmd.SetArgs([]string{"--name", exactName})

	// Passes name validation; fails later on sidecar ID resolution (no active sidecar).
	// We just confirm it does NOT return the length error.
	err := cmd.Execute()
	if err != nil {
		assert.Assert(t, !strings.Contains(err.Error(), "255 characters or fewer"),
			"unexpected length validation error for 255-char name: %v", err)
	}
}

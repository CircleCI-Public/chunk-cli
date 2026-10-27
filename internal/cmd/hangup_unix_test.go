//go:build !windows

package cmd

import (
	"syscall"
	"testing"
	"time"

	"gotest.tools/v3/assert"
)

// A closed terminal window sends SIGHUP; it must end the context, so the
// planning session is stopped, rather than kill chunk outright.
func TestUntilHangupEndsOnSIGHUP(t *testing.T) {
	ctx, stop := untilHangup(t.Context())
	t.Cleanup(stop)

	err := syscall.Kill(syscall.Getpid(), syscall.SIGHUP)
	assert.NilError(t, err)
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("SIGHUP did not end the context")
	}
}

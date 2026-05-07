package sidecar

import (
	"fmt"
	"net"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
)

func TestIsTransientSSHError(t *testing.T) {
	t.Run("timeout is transient", func(t *testing.T) {
		err := &net.OpError{Op: "dial", Err: &timeoutError{}}
		assert.Equal(t, isTransientSSHError(err), true)
	})

	t.Run("connection refused is transient", func(t *testing.T) {
		err := &net.OpError{Op: "dial", Net: "tcp", Err: fmt.Errorf("connection refused")}
		assert.Equal(t, isTransientSSHError(err), true)
	})

	t.Run("net error wrapped with fmt.Errorf is transient", func(t *testing.T) {
		inner := &net.OpError{Op: "dial", Err: &timeoutError{}}
		err := fmt.Errorf("register SSH key: %w", inner)
		assert.Equal(t, isTransientSSHError(err), true)
	})

	t.Run("ErrNotAuthorized is not transient", func(t *testing.T) {
		err := fmt.Errorf("add ssh key: %w", circleci.ErrNotAuthorized)
		assert.Equal(t, isTransientSSHError(err), false)
	})

	t.Run("StatusError is not transient", func(t *testing.T) {
		err := &circleci.StatusError{Op: "add ssh key", StatusCode: 503}
		assert.Equal(t, isTransientSSHError(err), false)
	})

	t.Run("KeyNotFoundError is not transient", func(t *testing.T) {
		err := &KeyNotFoundError{Path: "/home/user/.ssh/chunk_ai"}
		assert.Equal(t, isTransientSSHError(err), false)
	})

	t.Run("PublicKeyNotFoundError is not transient", func(t *testing.T) {
		err := &PublicKeyNotFoundError{KeyPath: "/home/user/.ssh/chunk_ai.pub"}
		assert.Equal(t, isTransientSSHError(err), false)
	})

	t.Run("generic error is not transient", func(t *testing.T) {
		err := fmt.Errorf("resolve home directory: permission denied")
		assert.Equal(t, isTransientSSHError(err), false)
	})
}

// timeoutError is a net.Error that reports Timeout() == true.
type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

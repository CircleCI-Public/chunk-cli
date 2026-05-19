package sidecar

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
)

func TestIsTransientSSHError(t *testing.T) {
	t.Run("timeout is transient", func(t *testing.T) {
		err := &net.OpError{Op: "dial", Err: &timeoutError{}}
		assert.Equal(t, isTransientSSHError(err), true)
	})

	t.Run("connection refused is transient", func(t *testing.T) {
		err := &net.OpError{
			Op:  "dial",
			Net: "tcp",
			Err: &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED},
		}
		assert.Equal(t, isTransientSSHError(err), true)
	})

	t.Run("connection refused wrapped with fmt.Errorf is transient", func(t *testing.T) {
		inner := &net.OpError{
			Op:  "dial",
			Net: "tcp",
			Err: &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED},
		}
		err := fmt.Errorf("websocket connect: %w", inner)
		assert.Equal(t, isTransientSSHError(err), true)
	})

	t.Run("timeout wrapped with fmt.Errorf is transient", func(t *testing.T) {
		inner := &net.OpError{Op: "dial", Err: &timeoutError{}}
		err := fmt.Errorf("register SSH key: %w", inner)
		assert.Equal(t, isTransientSSHError(err), true)
	})

	t.Run("unreachable host is not transient", func(t *testing.T) {
		err := &net.OpError{
			Op:  "dial",
			Net: "tcp",
			Err: &os.SyscallError{Syscall: "connect", Err: syscall.EHOSTUNREACH},
		}
		assert.Equal(t, isTransientSSHError(err), false)
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

func TestWaitForSSHReady(t *testing.T) {
	t.Run("succeeds immediately when SSH server is ready", func(t *testing.T) {
		keyFile, pubKey := fakes.GenerateSSHKeypair(t)
		sshSrv := fakes.NewSSHServer(t, pubKey)

		session := &Session{
			URL:          sshSrv.Addr(),
			IdentityFile: keyFile,
			KnownHosts:   filepath.Join(t.TempDir(), "known_hosts"),
		}

		var notified bool
		statusFn := iostream.StatusFunc(func(_ iostream.Level, _ string) { notified = true })

		err := waitForSSHReady(context.Background(), session, statusFn)
		assert.NilError(t, err)
		assert.Equal(t, notified, false, "no retry should be needed when SSH is already ready")
	})

	t.Run("permanent error returns immediately without notifying", func(t *testing.T) {
		permanentErr := errors.New("ssh handshake: auth failed")

		var notifications int
		statusFn := iostream.StatusFunc(func(_ iostream.Level, _ string) { notifications++ })

		err := waitForSSHReadyWithDial(context.Background(), &Session{}, statusFn,
			func(_ context.Context, _ *Session) (*sshConn, error) {
				return nil, permanentErr // not a net.Error → permanent
			},
		)
		assert.ErrorIs(t, err, permanentErr)
		assert.Equal(t, notifications, 0, "permanent error should not trigger retry notification")
	})

	t.Run("retries on transient error and notifies exactly once", func(t *testing.T) {
		transientErr := &net.OpError{
			Op:  "dial",
			Net: "tcp",
			Err: &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED},
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		var notifications int
		statusFn := iostream.StatusFunc(func(_ iostream.Level, _ string) {
			notifications++
			cancel() // stop retrying after the first notification
		})

		err := waitForSSHReadyWithDial(ctx, &Session{}, statusFn,
			func(_ context.Context, _ *Session) (*sshConn, error) {
				return nil, transientErr
			},
		)
		assert.Assert(t, err != nil, "should return an error when retries are stopped")
		assert.Equal(t, notifications, 1, "status should be notified exactly once regardless of retry count")
	})

	t.Run("succeeds after transient errors resolve", func(t *testing.T) {
		transientErr := &net.OpError{
			Op:  "dial",
			Net: "tcp",
			Err: &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED},
		}

		attempts := 0
		statusFn := iostream.StatusFunc(func(_ iostream.Level, _ string) {})

		err := waitForSSHReadyWithDial(context.Background(), &Session{}, statusFn,
			func(_ context.Context, _ *Session) (*sshConn, error) {
				attempts++
				if attempts < 3 {
					return nil, transientErr
				}
				return nil, nil // success: SSH is now ready
			},
		)
		assert.NilError(t, err)
		assert.Equal(t, attempts, 3, "should have retried until success")
	})
}

// timeoutError is a net.Error that reports Timeout() == true.
type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

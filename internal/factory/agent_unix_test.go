//go:build !windows

package factory

import (
	"errors"
	"syscall"
)

// processGone reports whether no process has pid.
func processGone(pid int) bool { return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) }

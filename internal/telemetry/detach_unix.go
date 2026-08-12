//go:build !windows

package telemetry

import (
	"os/exec"
	"syscall"
)

// detachProcess puts the child in its own process group so that signals sent
// to the parent's group (e.g. Ctrl-C) do not reach it.
func detachProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

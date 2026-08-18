//go:build windows

package telemetry

import (
	"os/exec"
	"syscall"
)

// detachProcess puts the child in a new process group so that console control
// events sent to the parent's group (e.g. Ctrl-C) do not reach it. This is the
// Windows analogue of setpgid.
func detachProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

//go:build !windows

package factory

import (
	"os/exec"
	"syscall"
)

// stopTree makes cancelling cmd kill everything it started, not only cmd:
// claude's own children (its shell commands, MCP servers) would otherwise
// carry on, holding its output open, after claude was stopped.
func stopTree(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}

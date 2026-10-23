//go:build !windows

package factory

import (
	"os"
	"os/exec"
	"syscall"
)

// agentProcAttr puts claude in a process group of its own, so the terminal's
// own Ctrl-C does not reach it: only Agent.Stop signals it.
func agentProcAttr(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

func interrupt(p *os.Process) error { return p.Signal(syscall.SIGINT) }
func terminate(p *os.Process) error { return p.Signal(syscall.SIGTERM) }

// killGroup SIGKILLs claude's whole process group, which agentProcAttr made
// claude the leader of, so the commands it started die with it.
func killGroup(p *os.Process) error { return syscall.Kill(-p.Pid, syscall.SIGKILL) }

//go:build windows

package factory

import (
	"os"
	"os/exec"
)

// agentProcAttr leaves the process attributes alone on Windows, where there is
// no process group to isolate claude's signals in.
func agentProcAttr(*exec.Cmd) {}

func interrupt(p *os.Process) error { return p.Kill() }
func terminate(p *os.Process) error { return p.Kill() }
func killGroup(p *os.Process) error { return p.Kill() }

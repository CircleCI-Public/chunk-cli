//go:build windows

package factory

import "os/exec"

// stopTree leaves cancelling cmd to the default kill on Windows, where there
// is no process group to signal; FirstPass's WaitDelay stops it waiting on
// children that outlive it.
func stopTree(*exec.Cmd) {}

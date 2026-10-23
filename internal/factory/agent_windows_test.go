//go:build windows

package factory

import "os"

// processGone reports whether no process has pid.
func processGone(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return true
	}
	_ = p.Release()
	return false
}

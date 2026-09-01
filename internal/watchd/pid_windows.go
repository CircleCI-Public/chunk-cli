//go:build windows

package watchd

import (
	"os"

	"golang.org/x/sys/windows"
)

// IsRunning reports whether the process whose PID is stored in path is alive.
// Returns (false, 0, nil) when the file doesn't exist.
//
// Signal(0) is unsupported on Windows, so we open a process handle and check
// its exit code instead.
func IsRunning(path string) (bool, int, error) {
	p, err := readPID(path)
	if os.IsNotExist(err) {
		return false, 0, nil
	}
	if err != nil {
		return false, 0, err
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(p))
	if err != nil {
		return false, 0, nil
	}
	defer windows.CloseHandle(h) //nolint:errcheck
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false, 0, nil
	}
	return code == windows.STILL_ACTIVE, p, nil
}

// terminate stops the process. Windows has no SIGTERM, so the process is killed
// outright; the next daemon to start removes the socket file it leaves behind.
func terminate(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return proc.Kill()
}

// Package browser opens URLs in the user's default browser.
package browser

import (
	"fmt"
	"os/exec"
	"runtime"
)

// Open starts the platform's opener for url and returns without waiting for it.
func Open(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Start()
	case "linux":
		return exec.Command("xdg-open", url).Start()
	case "windows":
		return exec.Command("cmd", "/c", "start", url).Start()
	default:
		return fmt.Errorf("unsupported platform %s", runtime.GOOS)
	}
}

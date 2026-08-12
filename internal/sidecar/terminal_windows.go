//go:build windows

package sidecar

import (
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/term"
)

// windowSizePollInterval is how often the local terminal is measured on
// Windows. SIGWINCH does not exist there, so resize is detected by polling.
const windowSizePollInterval = 250 * time.Millisecond

// watchWindowSize polls the local terminal size and updates the remote PTY
// when it changes. It returns when done is closed.
//
// This is the Windows counterpart to the SIGWINCH-driven implementation in
// terminal_unix.go.
func watchWindowSize(fd int, sess *ssh.Session, done <-chan struct{}) {
	ticker := time.NewTicker(windowSizePollInterval)
	defer ticker.Stop()

	lastW, lastH, err := term.GetSize(fd)
	if err != nil {
		// Not a measurable terminal; nothing useful to report.
		lastW, lastH = 0, 0
	}

	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			w, h, err := term.GetSize(fd)
			if err != nil {
				continue
			}
			if w == lastW && h == lastH {
				continue
			}
			lastW, lastH = w, h
			_ = sess.WindowChange(h, w)
		}
	}
}

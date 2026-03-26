package sandbox

import (
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/crypto/ssh"
	"golang.org/x/term"
)

// watchWindowSize listens for SIGWINCH and updates the remote PTY size.
// It returns when done is closed.
func watchWindowSize(fd int, sess *ssh.Session, done <-chan struct{}) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	defer signal.Stop(ch)

	for {
		select {
		case <-done:
			return
		case <-ch:
			w, h, err := term.GetSize(fd)
			if err != nil {
				continue
			}
			_ = sess.WindowChange(h, w)
		}
	}
}

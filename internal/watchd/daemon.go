package watchd

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
)

// RunDaemon is the watch daemon entry point, called by the hidden _daemon subcommand.
func RunDaemon(ctx context.Context) error {
	if _, err := EnsureDir(); err != nil {
		return fmt.Errorf("ensure watchd dir: %w", err)
	}

	pidPath, err := PIDPath()
	if err != nil {
		return err
	}
	if err := writePID(pidPath, os.Getpid()); err != nil {
		return fmt.Errorf("write pid: %w", err)
	}
	defer func() { _ = os.Remove(pidPath) }()

	sockPath, err := SocketPath()
	if err != nil {
		return err
	}
	_ = os.Remove(sockPath)

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", sockPath, err)
	}
	defer func() { _ = ln.Close() }()
	defer func() { _ = os.Remove(sockPath) }()

	ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, os.Interrupt)
	defer stop()

	log.Printf("watch daemon started pid=%d socket=%s", os.Getpid(), sockPath)

	srv := newServer()
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()

	if err := srv.Serve(ln); err != nil && ctx.Err() == nil {
		return fmt.Errorf("watchd serve: %w", err)
	}
	return nil
}

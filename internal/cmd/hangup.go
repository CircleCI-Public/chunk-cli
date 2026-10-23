package cmd

import (
	"context"
	"os/signal"
	"syscall"
)

// untilHangup is ctx, also cancelled when the terminal hangs up (SIGHUP, as
// when its window is closed). A command that starts processes of its own,
// such as a planning session in a process group of its own, uses it so
// closing the window stops them as quitting would; otherwise SIGHUP kills
// chunk before it can, and they outlive it. stop must be called.
func untilHangup(ctx context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(ctx, syscall.SIGHUP)
}

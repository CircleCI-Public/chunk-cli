package cmd

import (
	"context"
	"os/signal"
	"syscall"
)

// untilHangup is ctx, also cancelled when the terminal hangs up (SIGHUP, as
// when its window is closed), so closing the window quits cleanly rather than
// SIGHUP killing chunk mid-draw. stop must be called.
func untilHangup(ctx context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(ctx, syscall.SIGHUP)
}

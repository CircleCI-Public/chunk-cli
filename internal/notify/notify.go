package notify

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"time"
)

// Sender fires a desktop notification.
type Sender interface {
	Send(title, body string)
}

// DefaultSender is the process-wide Sender. Replace in tests with a spy.
var DefaultSender Sender = &osSender{}

// Send fires a desktop notification via DefaultSender. Errors are silently
// discarded — notifications are best-effort and must not disrupt the caller.
func Send(title, body string) {
	DefaultSender.Send(title, body)
}

type osSender struct{}

func (osSender) Send(title, body string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		script := fmt.Sprintf(`display notification %q with title %q`, body, title)
		cmd = exec.CommandContext(ctx, "osascript", "-e", script)
	case "linux":
		cmd = exec.CommandContext(ctx, "notify-send", title, body)
	case "windows":
		ps := fmt.Sprintf(
			`[System.Reflection.Assembly]::LoadWithPartialName('System.Windows.Forms') | Out-Null; `+
				`$n = New-Object System.Windows.Forms.NotifyIcon; `+
				`$n.Icon = [System.Drawing.SystemIcons]::Information; `+
				`$n.BalloonTipTitle = %q; $n.BalloonTipText = %q; `+
				`$n.Visible = $true; $n.ShowBalloonTip(5000)`,
			title, body,
		)
		cmd = exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", ps)
	default:
		return
	}
	_ = cmd.Run()
}

package notify

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// Send fires a desktop notification in the background. Errors are silently
// discarded — notifications are best-effort and must not disrupt the caller.
func Send(title, body string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		script := fmt.Sprintf(`display notification %s with title %s`, asQuote(body), asQuote(title))
		cmd = exec.Command("osascript", "-e", script)
	case "linux":
		cmd = exec.Command("notify-send", title, body)
	case "windows":
		ps := fmt.Sprintf(
			`[System.Reflection.Assembly]::LoadWithPartialName('System.Windows.Forms') | Out-Null; `+
				`$n = New-Object System.Windows.Forms.NotifyIcon; `+
				`$n.Icon = [System.Drawing.SystemIcons]::Information; `+
				`$n.BalloonTipTitle = %s; $n.BalloonTipText = %s; `+
				`$n.Visible = $true; $n.ShowBalloonTip(5000)`,
			psQuote(title), psQuote(body),
		)
		cmd = exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", ps)
	default:
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = cmd.Start()
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case <-ctx.Done():
			_ = cmd.Process.Kill()
		case <-done:
		}
	}()
}

// asQuote wraps s in an AppleScript double-quoted string literal, escaping
// internal double quotes by doubling them.
func asQuote(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// psQuote wraps s in a PowerShell single-quoted string literal, escaping
// internal single quotes by doubling them.
func psQuote(s string) string {
	return `'` + strings.ReplaceAll(s, `'`, `''`) + `'`
}

package cmd

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/monitor"
	"github.com/CircleCI-Public/chunk-cli/internal/monitor/ipc"
	"github.com/CircleCI-Public/chunk-cli/internal/monitor/pid"
)

// reportHookValidation sends the validate pass/fail result to the agent daemon.
// Called from finishValidate when running as a Stop hook. Errors are silently
// ignored — monitoring is best-effort and must never affect the hook exit code.
func reportHookValidation(sessionID string, passed bool) {
	go func() {
		ctx := context.Background()
		if err := ensureAgentRunning(ctx); err != nil {
			return
		}
		status := "passed"
		if !passed {
			status = "failed"
		}
		_ = sendToAgent(ipc.Request{
			Cmd:       ipc.CmdSetValidation,
			SessionID: sessionID,
			Payload:   map[string]any{"status": status},
			Timestamp: time.Now(),
		})
	}()
}

func sendToAgent(req ipc.Request) error {
	sockPath, err := monitor.SocketPath("agent")
	if err != nil {
		return err
	}
	conn, err := net.DialTimeout("unix", sockPath, 2*time.Second)
	if err != nil {
		return fmt.Errorf("connect to agent: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if err := ipc.Send(conn, req); err != nil {
		return fmt.Errorf("send event: %w", err)
	}
	resp, err := ipc.ReceiveResponse(conn)
	if err != nil {
		return fmt.Errorf("receive response: %w", err)
	}
	if !resp.OK {
		return fmt.Errorf("agent error: %s", resp.Error)
	}
	return nil
}

func launchDaemon(name string, subArgs []string) error {
	if _, err := monitor.EnsureDir(); err != nil {
		return fmt.Errorf("ensure monitor dir: %w", err)
	}
	logPath, err := monitor.LogPath(name)
	if err != nil {
		return err
	}
	logFile, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	defer func() { _ = logFile.Close() }()

	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("get executable: %w", err)
	}
	child := exec.Command(executable, subArgs...)
	child.Stdout = logFile
	child.Stderr = logFile
	child.Stdin = nil
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := child.Start(); err != nil {
		return fmt.Errorf("start %s daemon: %w", name, err)
	}

	pidPath, err := monitor.PIDPath(name)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ok, _, _ := pid.Running(pidPath)
		if ok {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("%s daemon did not start within 5s; check %s", name, logPath)
}

func ensureRunning(_ context.Context, name string, subArgs ...string) error {
	pidPath, err := monitor.PIDPath(name)
	if err != nil {
		return err
	}
	running, _, _ := pid.Running(pidPath)
	if running {
		return nil
	}
	return launchDaemon(name, subArgs)
}

func ensureServerRunning(ctx context.Context) error {
	return ensureRunning(ctx, "server", "watch", "_server-daemon")
}

func ensureAgentRunning(ctx context.Context) error {
	return ensureRunning(ctx, "agent", "watch", "_agent-daemon")
}

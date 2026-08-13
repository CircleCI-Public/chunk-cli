package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/monitor"
	"github.com/CircleCI-Public/chunk-cli/internal/monitor/agent"
	"github.com/CircleCI-Public/chunk-cli/internal/monitor/ipc"
	"github.com/CircleCI-Public/chunk-cli/internal/monitor/pid"
	"github.com/CircleCI-Public/chunk-cli/internal/monitor/server"
)

func newMonitorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "monitor",
		Short: "Monitor coding agent sessions",
		RunE:  runMonitorDashboard,
	}
	cmd.AddCommand(newMonitorStatusCmd())
	cmd.AddCommand(newMonitorServerCmd())
	cmd.AddCommand(newMonitorAgentCmd())
	return cmd
}

func runMonitorDashboard(cmd *cobra.Command, _ []string) error {
	if err := ensureServerRunning(cmd.Context()); err != nil {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not start server: %v\n", err)
	}
	m := server.Dashboard{}
	p := tea.NewProgram(m, tea.WithContext(cmd.Context()))
	_, err := p.Run()
	return err
}

// --- monitor status ---

func newMonitorStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show status of monitor daemons",
		RunE: func(cmd *cobra.Command, _ []string) error {
			printDaemonStatus(cmd.OutOrStdout(), "server")
			printDaemonStatus(cmd.OutOrStdout(), "agent")
			return nil
		},
	}
}

func printDaemonStatus(out io.Writer, name string) {
	pidPath, err := monitor.PIDPath(name)
	if err != nil {
		_, _ = fmt.Fprintf(out, "%s: error: %v\n", name, err)
		return
	}
	running, p, _ := pid.Running(pidPath)
	if running {
		_, _ = fmt.Fprintf(out, "%s: running (pid %d)\n", name, p)
	} else {
		_, _ = fmt.Fprintf(out, "%s: stopped\n", name)
	}
}

// --- monitor server ---

func newMonitorServerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "server",
		Short: "Manage the monitor server daemon",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(newMonitorServerStartCmd())
	cmd.AddCommand(newMonitorServerStopCmd())
	cmd.AddCommand(newMonitorServerStatusCmd())
	cmd.AddCommand(newMonitorServerLogsCmd())
	cmd.AddCommand(&cobra.Command{
		Use:    "_daemon",
		Hidden: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return server.RunDaemon(cmd.Context())
		},
	})
	return cmd
}

func newMonitorServerStartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "Start the monitor server daemon",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return startDaemon(cmd.OutOrStdout(), "server", "monitor", "server", "_daemon")
		},
	}
}

func newMonitorServerStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the monitor server daemon",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return stopDaemon(cmd.OutOrStdout(), "server")
		},
	}
}

func newMonitorServerStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show server daemon status",
		RunE: func(cmd *cobra.Command, _ []string) error {
			printDaemonStatus(cmd.OutOrStdout(), "server")
			return nil
		},
	}
}

func newMonitorServerLogsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logs",
		Short: "Print server daemon logs",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return showLogs(cmd.OutOrStdout(), "server")
		},
	}
}

// --- monitor agent ---

func newMonitorAgentCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Manage the monitor agent daemon",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(newMonitorAgentStartCmd())
	cmd.AddCommand(newMonitorAgentStopCmd())
	cmd.AddCommand(newMonitorAgentStatusCmd())
	cmd.AddCommand(newMonitorAgentLogsCmd())
	cmd.AddCommand(newMonitorAgentEventCmd())
	cmd.AddCommand(newMonitorAgentValidateCmd())
	cmd.AddCommand(&cobra.Command{
		Use:    "_daemon",
		Hidden: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return agent.RunDaemon(cmd.Context())
		},
	})
	return cmd
}

func newMonitorAgentStartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "Start the monitor agent daemon",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return startDaemon(cmd.OutOrStdout(), "agent", "monitor", "agent", "_daemon")
		},
	}
}

func newMonitorAgentStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the monitor agent daemon",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return stopDaemon(cmd.OutOrStdout(), "agent")
		},
	}
}

func newMonitorAgentStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show agent daemon status",
		RunE: func(cmd *cobra.Command, _ []string) error {
			printDaemonStatus(cmd.OutOrStdout(), "agent")
			return nil
		},
	}
}

func newMonitorAgentLogsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logs",
		Short: "Print agent daemon logs",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return showLogs(cmd.OutOrStdout(), "agent")
		},
	}
}

// monitorHookPayload is the JSON Claude Code delivers to every hook via stdin.
type monitorHookPayload struct {
	SessionID      string `json:"session_id"`
	ToolName       string `json:"tool_name"`        // set on PostToolUse
	StopHookActive bool   `json:"stop_hook_active"` // set on Stop
}

func (p monitorHookPayload) eventType() ipc.EventType {
	if p.StopHookActive {
		return ipc.EventSessionEnd
	}
	if p.ToolName != "" {
		return ipc.EventToolUse
	}
	return ipc.EventHeartbeat
}

func readHookPayload(r io.Reader) monitorHookPayload {
	raw, _ := io.ReadAll(r)
	var p monitorHookPayload
	_ = json.Unmarshal(raw, &p)
	if p.SessionID == "" {
		p.SessionID = os.Getenv("CLAUDE_SESSION_ID")
	}
	return p
}

func newMonitorAgentEventCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "event",
		Short: "Record a hook event (reads Claude Code hook payload from stdin)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			payload := readHookPayload(cmd.InOrStdin())
			if payload.SessionID == "" {
				return &userError{
					msg:    "Session ID required.",
					errMsg: "missing session_id in hook payload or CLAUDE_SESSION_ID",
				}
			}
			if err := ensureAgentRunning(cmd.Context()); err != nil {
				return fmt.Errorf("start agent daemon: %w", err)
			}
			if err := ensureServerRunning(cmd.Context()); err != nil {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not start server: %v\n", err)
			}
			return sendToAgent(ipc.Request{
				Cmd:        ipc.CmdEvent,
				SessionID:  payload.SessionID,
				EventType:  payload.eventType(),
				ToolName:   payload.ToolName,
				ProjectDir: os.Getenv("CLAUDE_PROJECT_DIR"),
				Timestamp:  time.Now(),
			})
		},
	}
}

func newMonitorAgentValidateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "Run chunk validate and record the result for this session",
		RunE: func(cmd *cobra.Command, _ []string) error {
			payload := readHookPayload(cmd.InOrStdin())

			// Send session_end before validate so the session is marked even if validate hangs.
			if payload.SessionID != "" {
				_ = ensureAgentRunning(cmd.Context())
				_ = sendToAgent(ipc.Request{
					Cmd:       ipc.CmdEvent,
					SessionID: payload.SessionID,
					EventType: ipc.EventSessionEnd,
					Timestamp: time.Now(),
				})
			}

			// Run validate using the same binary so dev builds test themselves.
			exe, err := os.Executable()
			if err != nil {
				return fmt.Errorf("get executable: %w", err)
			}
			validateCmd := exec.Command(exe, "validate")
			validateCmd.Stdout = cmd.OutOrStdout()
			validateCmd.Stderr = cmd.ErrOrStderr()
			validateErr := validateCmd.Run()

			// Record the result.
			if payload.SessionID != "" {
				status := "passed"
				if validateErr != nil {
					status = "failed"
				}
				_ = sendToAgent(ipc.Request{
					Cmd:       ipc.CmdSetValidation,
					SessionID: payload.SessionID,
					Payload:   map[string]any{"status": status},
					Timestamp: time.Now(),
				})
			}

			return validateErr
		},
	}
}

// --- shared helpers ---

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

func startDaemon(out io.Writer, name string, subArgs ...string) error {
	pidPath, err := monitor.PIDPath(name)
	if err != nil {
		return err
	}
	running, p, _ := pid.Running(pidPath)
	if running {
		_, _ = fmt.Fprintf(out, "%s already running (pid %d)\n", name, p)
		return nil
	}
	if err := launchDaemon(name, subArgs); err != nil {
		return err
	}
	_, p, _ = pid.Running(pidPath)
	_, _ = fmt.Fprintf(out, "%s started (pid %d)\n", name, p)
	return nil
}

func stopDaemon(out io.Writer, name string) error {
	pidPath, err := monitor.PIDPath(name)
	if err != nil {
		return err
	}
	running, _, _ := pid.Running(pidPath)
	if !running {
		_, _ = fmt.Fprintf(out, "%s not running\n", name)
		return nil
	}
	if err := pid.Kill(pidPath); err != nil {
		return fmt.Errorf("stop %s: %w", name, err)
	}
	_, _ = fmt.Fprintf(out, "%s stopped\n", name)
	return nil
}

func showLogs(out io.Writer, name string) error {
	logPath, err := monitor.LogPath(name)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(logPath)
	if os.IsNotExist(err) {
		_, _ = fmt.Fprintf(out, "no logs for %s\n", name)
		return nil
	}
	if err != nil {
		return fmt.Errorf("read logs: %w", err)
	}
	_, _ = out.Write(data)
	return nil
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
	return ensureRunning(ctx, "server", "monitor", "server", "_daemon")
}

func ensureAgentRunning(ctx context.Context) error {
	return ensureRunning(ctx, "agent", "monitor", "agent", "_daemon")
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

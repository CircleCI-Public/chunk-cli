// Package agent implements the monitor agent daemon.
package agent

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/monitor"
	"github.com/CircleCI-Public/chunk-cli/internal/monitor/ipc"
	"github.com/CircleCI-Public/chunk-cli/internal/monitor/pid"
)

// RunDaemon is the agent daemon entry point, called by the hidden _daemon subcommand.
func RunDaemon(ctx context.Context) error {
	if _, err := monitor.EnsureDir(); err != nil {
		return fmt.Errorf("ensure monitor dir: %w", err)
	}

	pidPath, err := monitor.PIDPath("agent")
	if err != nil {
		return err
	}
	if err := pid.Write(pidPath, os.Getpid()); err != nil {
		return fmt.Errorf("write pid: %w", err)
	}
	defer func() { _ = os.Remove(pidPath) }()

	sockPath, err := monitor.SocketPath("agent")
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

	statePath, err := monitor.AgentStatePath()
	if err != nil {
		return err
	}
	sf := newStateFile(statePath)

	ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, os.Interrupt)
	defer stop()

	log.Printf("agent daemon started pid=%d socket=%s", os.Getpid(), sockPath)

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			log.Printf("agent accept: %v", err)
			continue
		}
		go handleConn(ctx, sf, conn)
	}
}

func handleConn(ctx context.Context, sf *stateFile, conn net.Conn) {
	defer func() { _ = conn.Close() }()

	req, err := ipc.Receive(conn)
	if err != nil {
		log.Printf("agent receive: %v", err)
		return
	}

	resp := dispatch(ctx, sf, req)
	if err := ipc.SendResponse(conn, resp); err != nil {
		log.Printf("agent send response: %v", err)
	}
}

func dispatch(_ context.Context, sf *stateFile, req ipc.Request) ipc.Response {
	switch req.Cmd {
	case ipc.CmdPing:
		return ipc.Response{OK: true}

	case ipc.CmdEvent:
		if req.SessionID == "" {
			return ipc.Response{OK: false, Error: "missing session_id"}
		}
		if req.Timestamp.IsZero() {
			req.Timestamp = time.Now()
		}
		if err := sf.update(req); err != nil {
			log.Printf("update state: %v", err)
		}
		go forwardToServer(req)
		return ipc.Response{OK: true}

	case ipc.CmdSetValidation:
		if req.SessionID == "" {
			return ipc.Response{OK: false, Error: "missing session_id"}
		}
		status, _ := req.Payload["status"].(string)
		if err := sf.setValidation(req.SessionID, status); err != nil {
			log.Printf("set validation: %v", err)
		}
		go forwardToServer(req)
		return ipc.Response{OK: true}

	case ipc.CmdListSessions:
		sessions, err := sf.list()
		if err != nil {
			return ipc.Response{OK: false, Error: err.Error()}
		}
		return ipc.Response{OK: true, Sessions: sessions}

	case ipc.CmdGetEvents:
		return ipc.Response{OK: false, Error: "get_events is not supported by the agent"}

	case ipc.CmdGetSession, ipc.CmdAckConflict:
		// Forward directly to the server — these operate on DB-backed fields
		// that the agent state file doesn't track.
		resp, err := forwardAndReceive(req)
		if err != nil {
			return ipc.Response{OK: false, Error: err.Error()}
		}
		return resp
	default:
		return ipc.Response{OK: false, Error: fmt.Sprintf("unknown command %q", req.Cmd)}
	}
}

func forwardToServer(req ipc.Request) {
	if _, err := forwardAndReceive(req); err != nil {
		log.Printf("forward: %v", err)
	}
}

func forwardAndReceive(req ipc.Request) (ipc.Response, error) {
	sockPath, err := monitor.SocketPath("server")
	if err != nil {
		return ipc.Response{}, fmt.Errorf("socket path: %w", err)
	}
	conn, err := net.DialTimeout("unix", sockPath, 2*time.Second)
	if err != nil {
		return ipc.Response{}, fmt.Errorf("connect to server: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if err := ipc.Send(conn, req); err != nil {
		return ipc.Response{}, fmt.Errorf("send: %w", err)
	}
	resp, err := ipc.ReceiveResponse(conn)
	if err != nil {
		return ipc.Response{}, fmt.Errorf("response: %w", err)
	}
	return resp, nil
}

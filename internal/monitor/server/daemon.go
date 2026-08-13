// Package server implements the persistent monitor server daemon.
package server

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/CircleCI-Public/chunk-cli/internal/monitor"
	"github.com/CircleCI-Public/chunk-cli/internal/monitor/ipc"
	"github.com/CircleCI-Public/chunk-cli/internal/monitor/pid"
)

// RunDaemon is the server daemon entry point, called by the hidden _daemon subcommand.
func RunDaemon(ctx context.Context) error {
	if _, err := monitor.EnsureDir(); err != nil {
		return fmt.Errorf("ensure monitor dir: %w", err)
	}

	pidPath, err := monitor.PIDPath("server")
	if err != nil {
		return err
	}
	if err := pid.Write(pidPath, os.Getpid()); err != nil {
		return fmt.Errorf("write pid: %w", err)
	}
	defer func() { _ = os.Remove(pidPath) }()

	sockPath, err := monitor.SocketPath("server")
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

	dbPath, err := monitor.DBPath()
	if err != nil {
		return err
	}
	db, err := openDB(dbPath)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, os.Interrupt)
	defer stop()

	log.Printf("server daemon started pid=%d socket=%s", os.Getpid(), sockPath)

	startGitChecker(ctx, db)

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
			log.Printf("server accept: %v", err)
			continue
		}
		go handleConn(ctx, db, conn)
	}
}

func handleConn(ctx context.Context, db *sql.DB, conn net.Conn) {
	defer func() { _ = conn.Close() }()

	req, err := ipc.Receive(conn)
	if err != nil {
		log.Printf("server receive: %v", err)
		return
	}

	resp := dispatch(ctx, db, req)
	if err := ipc.SendResponse(conn, resp); err != nil {
		log.Printf("server send response: %v", err)
	}
}

const errMissingSessionID = "missing session_id"

func dispatch(ctx context.Context, db *sql.DB, req ipc.Request) ipc.Response {
	switch req.Cmd {
	case ipc.CmdPing:
		return ipc.Response{OK: true}

	case ipc.CmdEvent:
		if req.SessionID == "" {
			return ipc.Response{OK: false, Error: errMissingSessionID}
		}
		if err := upsertSession(ctx, db, req); err != nil {
			log.Printf("upsert session: %v", err)
			return ipc.Response{OK: false, Error: err.Error()}
		}
		if err := insertEvent(ctx, db, req); err != nil {
			log.Printf("insert event: %v", err)
		}
		return ipc.Response{OK: true}

	case ipc.CmdSetValidation:
		if req.SessionID == "" {
			return ipc.Response{OK: false, Error: errMissingSessionID}
		}
		status, _ := req.Payload["status"].(string)
		if status == "" {
			return ipc.Response{OK: false, Error: "missing status in payload"}
		}
		if err := setValidationStatus(ctx, db, req.SessionID, status); err != nil {
			return ipc.Response{OK: false, Error: err.Error()}
		}
		return ipc.Response{OK: true}

	case ipc.CmdListSessions:
		sessions, err := listSessions(ctx, db)
		if err != nil {
			return ipc.Response{OK: false, Error: err.Error()}
		}
		return ipc.Response{OK: true, Sessions: sessions}

	case ipc.CmdGetEvents:
		if req.SessionID == "" {
			return ipc.Response{OK: false, Error: errMissingSessionID}
		}
		events, err := getEvents(ctx, db, req.SessionID)
		if err != nil {
			return ipc.Response{OK: false, Error: err.Error()}
		}
		return ipc.Response{OK: true, Events: events}

	default:
		return ipc.Response{OK: false, Error: fmt.Sprintf("unknown command %q", req.Cmd)}
	}
}

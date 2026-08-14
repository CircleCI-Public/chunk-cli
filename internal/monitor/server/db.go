package server

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite" // register sqlite driver

	"github.com/CircleCI-Public/chunk-cli/internal/monitor/ipc"
)

const (
	statusActive = "active"
	statusEnded  = "ended"
)

const schema = `
CREATE TABLE IF NOT EXISTS sessions (
	id                  TEXT    PRIMARY KEY,
	project_dir         TEXT    NOT NULL DEFAULT '',
	started_at          TEXT    NOT NULL,
	last_seen_at        TEXT    NOT NULL,
	status              TEXT    NOT NULL DEFAULT 'active',
	validation_status   TEXT    NOT NULL DEFAULT '',
	tool_use_count      INTEGER NOT NULL DEFAULT 0,
	git_status          TEXT    NOT NULL DEFAULT '',
	conflict_notified   INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS events (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	session_id  TEXT    NOT NULL,
	event_type  TEXT    NOT NULL,
	tool_name   TEXT    NOT NULL DEFAULT '',
	occurred_at TEXT    NOT NULL
);
`

// alterations adds new columns to existing tables without breaking old DBs.
var alterations = []string{
	"ALTER TABLE sessions ADD COLUMN project_dir TEXT NOT NULL DEFAULT ''",
	"ALTER TABLE sessions ADD COLUMN tool_use_count INTEGER NOT NULL DEFAULT 0",
	"ALTER TABLE sessions ADD COLUMN git_status TEXT NOT NULL DEFAULT ''",
	"ALTER TABLE sessions ADD COLUMN conflict_notified INTEGER NOT NULL DEFAULT 0",
	"ALTER TABLE events ADD COLUMN tool_name TEXT NOT NULL DEFAULT ''",
}

func openDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate db: %w", err)
	}
	for _, stmt := range alterations {
		if _, err := db.Exec(stmt); err != nil {
			if !strings.Contains(err.Error(), "duplicate column name") {
				_ = db.Close()
				return nil, fmt.Errorf("alter table: %w", err)
			}
		}
	}
	return db, nil
}

func upsertSession(ctx context.Context, db *sql.DB, req ipc.Request) error {
	ts := req.Timestamp.UTC().Format(time.RFC3339)
	status := statusActive
	if req.EventType == ipc.EventSessionEnd {
		status = statusEnded
	}
	toolUseInc := 0
	if req.EventType == ipc.EventToolUse {
		toolUseInc = 1
	}
	_, err := db.ExecContext(ctx, `
		INSERT INTO sessions (id, project_dir, started_at, last_seen_at, status, tool_use_count)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			last_seen_at   = excluded.last_seen_at,
			project_dir    = CASE WHEN excluded.project_dir != '' THEN excluded.project_dir ELSE sessions.project_dir END,
			status         = excluded.status,
			tool_use_count = sessions.tool_use_count + ?
	`, req.SessionID, req.ProjectDir, ts, ts, status, toolUseInc, toolUseInc)
	return err
}

func insertEvent(ctx context.Context, db *sql.DB, req ipc.Request) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO events (session_id, event_type, tool_name, occurred_at)
		VALUES (?, ?, ?, ?)
	`, req.SessionID, string(req.EventType), req.ToolName, req.Timestamp.UTC().Format(time.RFC3339))
	return err
}

func setValidationStatus(ctx context.Context, db *sql.DB, sessionID, status string) error {
	_, err := db.ExecContext(ctx,
		`UPDATE sessions SET validation_status = ? WHERE id = ?`,
		status, sessionID)
	return err
}

const staleThreshold = 30 * time.Minute

func listSessions(ctx context.Context, db *sql.DB) ([]ipc.Session, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, project_dir, started_at, last_seen_at, status, validation_status,
		       tool_use_count, git_status, conflict_notified
		FROM sessions
		ORDER BY last_seen_at DESC
		LIMIT 100
	`)
	if err != nil {
		return nil, fmt.Errorf("query sessions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	now := time.Now()
	var sessions []ipc.Session
	for rows.Next() {
		var s ipc.Session
		var startedAt, lastSeenAt string
		if err := rows.Scan(&s.ID, &s.ProjectDir, &startedAt, &lastSeenAt,
			&s.Status, &s.ValidationStatus, &s.ToolUseCount, &s.GitStatus, &s.ConflictNotified); err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		s.StartedAt, _ = time.Parse(time.RFC3339, startedAt)
		s.LastSeenAt, _ = time.Parse(time.RFC3339, lastSeenAt)
		if s.Status == statusActive && now.Sub(s.LastSeenAt) > staleThreshold {
			s.Status = "stale"
		}
		sessions = append(sessions, s)
	}
	return sessions, rows.Err()
}

func setGitStatus(ctx context.Context, db *sql.DB, sessionID, status string) error {
	// Reset conflict_notified whenever the status moves away from "conflict"
	// so the next conflict event triggers a fresh notification.
	_, err := db.ExecContext(ctx, `
		UPDATE sessions SET
			git_status = ?,
			conflict_notified = CASE WHEN ? = 'conflict' THEN conflict_notified ELSE 0 END
		WHERE id = ?`, status, status, sessionID)
	return err
}

func setConflictNotified(ctx context.Context, db *sql.DB, sessionID string) error {
	_, err := db.ExecContext(ctx,
		`UPDATE sessions SET conflict_notified = 1 WHERE id = ?`, sessionID)
	return err
}

// sessionsWithDir returns all sessions that have a project_dir set,
// used by the background git status checker.
func sessionsWithDir(ctx context.Context, db *sql.DB) ([]ipc.Session, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, project_dir FROM sessions
		WHERE project_dir != ''
	`)
	if err != nil {
		return nil, fmt.Errorf("query active sessions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var sessions []ipc.Session
	for rows.Next() {
		var s ipc.Session
		if err := rows.Scan(&s.ID, &s.ProjectDir); err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		sessions = append(sessions, s)
	}
	return sessions, rows.Err()
}

func getEvents(ctx context.Context, db *sql.DB, sessionID string) ([]ipc.Event, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, session_id, event_type, tool_name, occurred_at
		FROM events
		WHERE session_id = ?
		ORDER BY id ASC
		LIMIT 500
	`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("query events: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var events []ipc.Event
	for rows.Next() {
		var e ipc.Event
		var occurredAt string
		if err := rows.Scan(&e.ID, &e.SessionID, &e.EventType, &e.ToolName, &occurredAt); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		e.OccurredAt, _ = time.Parse(time.RFC3339, occurredAt)
		events = append(events, e)
	}
	return events, rows.Err()
}

func getSession(ctx context.Context, db *sql.DB, sessionID string) (ipc.Session, error) {
	var s ipc.Session
	var startedAt, lastSeenAt string
	err := db.QueryRowContext(ctx, `
		SELECT id, project_dir, started_at, last_seen_at, status, validation_status,
		       tool_use_count, git_status, conflict_notified
		FROM sessions WHERE id = ?`, sessionID).
		Scan(&s.ID, &s.ProjectDir, &startedAt, &lastSeenAt,
			&s.Status, &s.ValidationStatus, &s.ToolUseCount, &s.GitStatus, &s.ConflictNotified)
	if err != nil {
		return ipc.Session{}, fmt.Errorf("get session: %w", err)
	}
	s.StartedAt, _ = time.Parse(time.RFC3339, startedAt)
	s.LastSeenAt, _ = time.Parse(time.RFC3339, lastSeenAt)
	return s, nil
}

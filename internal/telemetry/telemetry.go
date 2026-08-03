// Package telemetry sends anonymous command-usage events to Segment.
//
// Telemetry is opt-out: it fires unless disabled via a well-known opt-out
// environment variable or the persisted telemetry config preference (see
// internal/config.IsTelemetry).
// Only the command path, the names (never values) of flags the user set,
// whether the command succeeded, the Go type and message of any error, a
// per-install anonymous instance ID, the operating system, and the detected
// AI coding agent (if any) are ever collected — no flag values, argument
// values, or other PII.
//
// Modeled on circleci-cli's internal/telemetry package.
package telemetry

import (
	"errors"
	"io"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/segmentio/analytics-go/v3"
)

// Sender tracks anonymous command-usage events. A nil *Sender is valid and
// silently drops events, so callers never need to nil-check it.
type Sender struct {
	dest destination
	meta Meta

	closed atomic.Bool
}

type destination interface {
	io.Closer
	Enqueue(track analytics.Track) error
}

// Config configures a Sender.
type Config struct {
	// Send enables sending events to Segment via the delegate subprocess.
	Send bool
	// Log enables logging events to stderr for debugging.
	Log bool

	// Binary is re-exec'd with JSON-encoded events on stdin when Send is true.
	Binary string
	// WriteKey is the Segment write key used by the delegate subprocess.
	WriteKey string
	// Endpoint is the Segment endpoint. Optional; defaults to segment.io.
	// Normally only set for testing.
	Endpoint string

	// TestDestination, when non-nil, is added as an event destination so
	// tests can record events and assert on them synchronously without
	// spawning a subprocess or hitting the network.
	TestDestination destination

	Metadata Meta
}

// Meta describes the fields attached to every tracked event.
type Meta struct {
	Version    string
	InstanceID uuid.UUID

	// OS is the operating system chunk-cli is running on, e.g. runtime.GOOS.
	OS string
	// CodingAgent is the AI coding agent chunk-cli was invoked from (e.g.
	// "claude-code", "cursor"), or "" if none was detected. See
	// DetectCodingAgent.
	CodingAgent string
}

func (m *Meta) toContext() *analytics.Context {
	ctx := &analytics.Context{
		App: analytics.AppInfo{
			Name:    "chunk-cli",
			Version: m.Version,
		},
		Device: analytics.DeviceInfo{Id: m.InstanceID.String()},
		OS:     analytics.OSInfo{Name: m.OS},
	}
	if m.CodingAgent != "" {
		ctx.Extra = map[string]interface{}{"codingAgent": m.CodingAgent}
	}
	return ctx
}

// NewSender creates a new Sender per cfg.
func NewSender(cfg Config) (*Sender, error) {
	dest := &multiDestination{}

	if cfg.TestDestination != nil {
		dest.Add(cfg.TestDestination)
	}

	if cfg.Log {
		dest.Add(&loggingDestination{})
	}

	if cfg.Send {
		if cfg.Binary == "" {
			return nil, errors.New("binary is required")
		}
		dest.Add(&delegateDestination{
			bin:      cfg.Binary,
			writeKey: cfg.WriteKey,
			endpoint: cfg.Endpoint,
		})
	}

	return &Sender{
		dest: dest,
		meta: cfg.Metadata,
	}, nil
}

// Close flushes buffered events to their destinations. Safe to call multiple
// times and on a nil Sender.
func (s *Sender) Close() error {
	if s == nil {
		return nil
	}
	if s.closed.Swap(true) {
		return nil
	}
	return s.dest.Close()
}

// Track records an analytics event. Safe to call on a nil Sender.
func (s *Sender) Track(eventName string, props map[string]any) error {
	if s == nil {
		return nil
	}

	p := analytics.NewProperties()
	for key, val := range props {
		p.Set(key, val)
	}

	return s.dest.Enqueue(analytics.Track{
		Event:      eventName,
		Timestamp:  time.Now(),
		Properties: p,

		UserId:  s.meta.InstanceID.String(),
		Context: s.meta.toContext(),
	})
}

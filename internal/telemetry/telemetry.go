// Package telemetry sends anonymous command-usage events to Segment.
//
// Telemetry is opt-out: it fires unless disabled via a well-known opt-out
// environment variable or the persisted telemetry config preference (see
// internal/config.IsTelemetry).
// Only the command path, the names (never values) of flags the user set,
// the outcome ("success"/"failure"), the wall-clock duration, the Go type and
// message of any error, a per-install anonymous instance ID, the operating
// system, and the detected AI coding agent (if any) are ever collected — no
// flag values, argument values, or other PII.
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
	"github.com/shirou/gopsutil/v4/host"
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
	// SessionTrackingID, when non-zero, is used as AnonymousId so all chunk
	// invocations within a session share a common anonymous identifier.
	// When zero, InstanceID is used as AnonymousId instead.
	SessionTrackingID uuid.UUID
	// UserID is the authenticated CircleCI user UUID. When non-zero it is sent
	// as UserId so events can be attributed to a real user.
	UserID uuid.UUID

	// HostInfo is the host info to associate with events. When non-nil, OS.Name,
	// OS.Version, Device.Model (kernel arch), and Device.Type (platform family)
	// are populated from it. Best-effort: may be nil when host detection fails.
	HostInfo *host.InfoStat
	// Extra is forwarded to Context.Traits on every event (e.g. "agent", "is_tty").
	Extra map[string]any
}

func (m *Meta) toContext() *analytics.Context {
	var osInfo analytics.OSInfo
	device := analytics.DeviceInfo{Id: m.InstanceID.String()}
	if m.HostInfo != nil {
		osInfo = analytics.OSInfo{
			Name:    m.HostInfo.OS,
			Version: m.HostInfo.PlatformVersion,
		}
		device.Model = m.HostInfo.KernelArch
		device.Type = m.HostInfo.PlatformFamily
	}
	var traits map[string]any
	for k, v := range m.Extra {
		if s, ok := v.(string); ok && s == "" {
			continue
		}
		if traits == nil {
			traits = make(map[string]any)
		}
		traits[k] = v
	}
	return &analytics.Context{
		App:    analytics.AppInfo{Name: "chunk-cli", Version: m.Version},
		OS:     osInfo,
		Device: device,
		Traits: traits,
	}
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

	anonymousID := s.meta.InstanceID
	if s.meta.SessionTrackingID != uuid.Nil {
		anonymousID = s.meta.SessionTrackingID
	}
	track := analytics.Track{
		Event:        eventName,
		Timestamp:    time.Now(),
		Properties:   p,
		AnonymousId:  anonymousID.String(),
		Context:      s.meta.toContext(),
		Integrations: analytics.NewIntegrations().Enable("Amplitude"),
	}
	if s.meta.UserID != uuid.Nil {
		track.UserId = s.meta.UserID.String()
	}
	return s.dest.Enqueue(track)
}

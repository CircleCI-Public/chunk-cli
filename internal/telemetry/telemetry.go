// Package telemetry sends command-usage events to Segment.
//
// Telemetry is opt-out: it fires unless disabled via a well-known opt-out
// environment variable or the persisted telemetry config preference (see
// internal/config.IsTelemetry).
// Only the command path, the names (never values) of flags the user set,
// the outcome ("success"/"failure"), the wall-clock duration, the Go type and
// message of any error, a per-install anonymous instance ID, the operating
// system, the detected AI coding agent (if any), and — once the user has
// authenticated — their CircleCI user UUID are ever collected. No flag values,
// argument values, names, email addresses, or other PII.
//
// Events are therefore anonymous only until the user authenticates. From then
// on they carry the CircleCI user UUID, and an identify call joins the
// anonymous IDs the install was reporting under to that user, so the journey
// before logging in is not attributed to a stranger. An install that was
// already authenticated before its user ID was recorded is backfilled the
// next time it resolves a CircleCI client. See Sender.Identify and
// IdentifyUser.
//
// Modeled on circleci-cli's internal/telemetry package.
package telemetry

import (
	"errors"
	"io"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/segmentio/analytics-go/v3"
)

// Sender tracks command-usage events. A nil *Sender is valid and
// silently drops events, so callers never need to nil-check it.
type Sender struct {
	dest destination

	// mu guards meta, which SetUserID mutates mid-run when the user
	// authenticates while a command is already in flight.
	mu   sync.RWMutex
	meta Meta

	closed atomic.Bool
}

type destination interface {
	io.Closer
	// Enqueue accepts any Segment message — a track for a command
	// invocation, or an identify joining an anonymous ID to a user ID.
	Enqueue(msg analytics.Message) error
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

	// OSName, OSVersion, KernelArch, and PlatformFamily populate the Segment
	// OS and Device context fields. Best-effort: zero values are sent as-is.
	OSName         string
	OSVersion      string
	KernelArch     string
	PlatformFamily string
	// Extra is forwarded to Context.Traits on every event (e.g. "agent", "is_tty").
	Extra map[string]any
}

// anonymousID is the identifier events are attributed to before (and
// alongside) any user ID: the session tracking ID when one is known, so every
// invocation in an agent session shares it, and the per-install instance ID
// otherwise.
func (m *Meta) anonymousID() uuid.UUID {
	if m.SessionTrackingID != uuid.Nil {
		return m.SessionTrackingID
	}
	return m.InstanceID
}

// anonymousIDs returns every non-zero anonymous identifier this install
// reports under, deduplicated: the current one plus, inside an agent session,
// the instance ID that runs outside a session report under.
func (m *Meta) anonymousIDs() []uuid.UUID {
	ids := make([]uuid.UUID, 0, 2)
	for _, id := range []uuid.UUID{m.anonymousID(), m.InstanceID} {
		if id != uuid.Nil && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	return ids
}

func (m *Meta) toContext() *analytics.Context {
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
		App: analytics.AppInfo{Name: "chunk-cli", Version: m.Version},
		OS:  analytics.OSInfo{Name: m.OSName, Version: m.OSVersion},
		Device: analytics.DeviceInfo{
			Id:    m.InstanceID.String(),
			Model: m.KernelArch,
			Type:  m.PlatformFamily,
		},
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

	s.mu.RLock()
	meta := s.meta
	s.mu.RUnlock()

	track := analytics.Track{
		Event:        eventName,
		Timestamp:    time.Now(),
		Properties:   p,
		AnonymousId:  meta.anonymousID().String(),
		Context:      meta.toContext(),
		Integrations: analytics.NewIntegrations().Enable("Amplitude"),
	}
	if meta.UserID != uuid.Nil {
		track.UserId = meta.UserID.String()
	}
	return s.dest.Enqueue(track)
}

// SetUserID attaches userID to every event this Sender reports from now on,
// including the in-flight command_invocation. Safe to call on a nil Sender.
func (s *Sender) SetUserID(userID uuid.UUID) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.meta.UserID = userID
}

// Identify sends a Segment identify joining this install's anonymous ID(s) to
// userID. One call goes out per anonymous ID (session and/or instance), so
// both the in-session and out-of-session histories join the user. No traits
// are sent. Safe to call on a nil or closed Sender; uuid.Nil is a no-op.
func (s *Sender) Identify(userID uuid.UUID) error {
	if s == nil || userID == uuid.Nil || s.closed.Load() {
		return nil
	}

	s.mu.RLock()
	meta := s.meta
	s.mu.RUnlock()

	now := time.Now()
	errs := make([]error, 0, 2)
	for _, anonymousID := range meta.anonymousIDs() {
		errs = append(errs, s.dest.Enqueue(analytics.Identify{
			Timestamp:   now,
			UserId:      userID.String(),
			AnonymousId: anonymousID.String(),
			Context:     meta.toContext(),
		}))
	}
	return errors.Join(errs...)
}

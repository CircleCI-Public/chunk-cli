package telemetry

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/segmentio/analytics-go/v3"
)

type Client struct {
	client analytics.Client
	user   User
}

type Config struct {
	// WriteKey is the Segment write key, and if not provided, will disable telemetry.
	WriteKey string
	// Endpoint is the Segment endpoint, and is optional, defaulting to segment.io.
	// This is normally only set for testing.
	Endpoint string
	// Specifies the number of events to batch together before sending. If zero the client will use a default.
	// This is likely only useful for testing.
	BatchSize int

	// User is the user to associate with events.
	User User
}

type User struct {
	// InstanceID allows manually specifying the client instance ID. Not meant to be used in production, but
	// useful for deterministic tests.
	InstanceID uuid.UUID
	// UserID is the user ID to associate with events.
	UserID uuid.UUID

	OS      string
	Version string
}

func (u User) toContext() *analytics.Context {
	return &analytics.Context{
		App: analytics.AppInfo{
			Name:    "chunk",
			Version: u.Version,
		},
		OS: analytics.OSInfo{
			Name: u.OS,
		},
		Device: analytics.DeviceInfo{
			Id:           u.InstanceID.String(),
			Manufacturer: "CircleCI Ltd",
			Name:         "chunk",
		},
	}
}

// New creates a new segment client
func New(cfg Config) (*Client, error) {
	if cfg.WriteKey == "" {
		return nil, nil
	}

	client, err := analytics.NewWithConfig(cfg.WriteKey, analytics.Config{
		Endpoint:  cfg.Endpoint,
		BatchSize: cfg.BatchSize,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create segment client: %w", err)
	}

	if cfg.User.InstanceID == uuid.Nil {
		cfg.User.InstanceID = uuid.New()
	}

	if cfg.User.UserID == uuid.Nil {
		cfg.User.UserID = AnonymousID
	}

	return &Client{
		client: client,
		user:   cfg.User,
	}, nil
}

func (c *Client) Identify() error {
	if c == nil {
		return nil
	}

	return c.client.Enqueue(analytics.Identify{
		UserId:       c.user.UserID.String(),
		Context:      c.user.toContext(),
		Integrations: analytics.NewIntegrations().Enable("Amplitude"),
	})
}

func (c *Client) Close() error {
	if c == nil {
		return nil
	}
	return c.client.Close()
}

// AnonymousID is hard-coded to a well-known value for unknown users.
// Callers should provide a real user id where possible.
var AnonymousID = uuid.MustParse("7c9e6679-7425-40de-944b-e07fc1f90ae7")

// Track sends an analytics event.
func (c *Client) Track(eventName string, props map[string]any) error {
	// Allow a nil client so that it can be treated as non-critical, especially during tests.
	if c == nil {
		return nil
	}

	extras := analytics.NewProperties()

	for key, val := range props {
		extras.Set(key, val)
	}

	track := analytics.Track{
		Event:      eventName,
		Timestamp:  time.Now(),
		Properties: extras,

		UserId:       c.user.UserID.String(),
		Context:      c.user.toContext(),
		Integrations: analytics.NewIntegrations().Enable("Amplitude"),
	}

	return c.client.Enqueue(track)
}

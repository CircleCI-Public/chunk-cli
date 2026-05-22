package telemetry

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/segmentio/analytics-go/v3"
)

type Client struct {
	client analytics.Client
	user   User
}

type Mode int

const (
	// ModeNOOP disables telemetry; all operations are silent no-ops.
	ModeNOOP Mode = iota
	// ModeSend sends events to Segment.
	ModeSend
	// ModeLog logs events to stderr instead of sending them.
	ModeLog
)

type Config struct {
	Mode Mode

	// WriteKey is the Segment write key, required for ModeSend.
	WriteKey string
	// Endpoint is the Segment endpoint (optional, defaults to segment.io).
	Endpoint string
	// BatchSize controls how many events are batched before sending. Only useful for testing.
	BatchSize int

	User User
}

type User struct {
	// InstanceID is a stable identifier for this device. A random UUID is generated if empty.
	InstanceID     string
	OrganizationID string
	OS             string
	Arch           string
	Version        string
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
			Id:           u.InstanceID,
			Manufacturer: "CircleCI Ltd",
			Name:         "chunk",
		},
		Extra: map[string]interface{}{
			"arch": u.Arch,
		},
	}
}

// New creates a telemetry client. The returned client is never nil;
// when telemetry is disabled or unavailable a no-op client is used.
func New(ctx context.Context, cfg Config) (*Client, error) {
	var client analytics.Client
	switch cfg.Mode {
	case ModeNOOP:
		client = noopClient{}
	case ModeSend:
		if cfg.WriteKey == "" {
			return nil, fmt.Errorf("write key is required for ModeSend")
		}
		var err error
		client, err = analytics.NewWithConfig(cfg.WriteKey, analytics.Config{
			Endpoint:  cfg.Endpoint,
			BatchSize: cfg.BatchSize,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create segment client: %w", err)
		}
	case ModeLog:
		client = newLoggingClient(ctx)
	}

	if cfg.User.InstanceID == "" {
		cfg.User.InstanceID = uuid.NewString()
	}

	return &Client{
		client: client,
		user:   cfg.User,
	}, nil
}

func (c *Client) Identify() error {
	return c.client.Enqueue(analytics.Identify{
		AnonymousId:  c.user.InstanceID,
		Context:      c.user.toContext(),
		Integrations: analytics.NewIntegrations().Enable("Amplitude"),
	})
}

func (c *Client) Close() error {
	return c.client.Close()
}

// Track sends an analytics event.
func (c *Client) Track(eventName string, props map[string]any) error {
	extras := analytics.NewProperties()
	extras.Set("sender", "chunk-cli")
	extras.Set("team_name", "factory")
	if c.user.OrganizationID != "" {
		extras.Set("organization_id", c.user.OrganizationID)
	}
	for key, val := range props {
		extras.Set(key, val)
	}

	return c.client.Enqueue(analytics.Track{
		Event:        eventName,
		Timestamp:    time.Now(),
		Properties:   extras,
		AnonymousId:  c.user.InstanceID,
		Context:      c.user.toContext(),
		Integrations: analytics.NewIntegrations().Enable("Amplitude"),
	})
}

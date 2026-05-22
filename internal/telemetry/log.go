package telemetry

import (
	"context"
	"fmt"
	"os"

	"github.com/segmentio/analytics-go/v3"
)

type loggingClient struct {
	ctx context.Context
}

func newLoggingClient(ctx context.Context) *loggingClient {
	return &loggingClient{ctx: ctx}
}

func (l *loggingClient) Close() error { return nil }

func (l *loggingClient) Enqueue(m analytics.Message) error {
	switch m := m.(type) {
	case analytics.Track:
		fmt.Fprintf(os.Stderr, "[telemetry] track %s %v\n", m.Event, m.Properties)
	case analytics.Identify:
		fmt.Fprintf(os.Stderr, "[telemetry] identify %s\n", m.AnonymousId)
	}
	return nil
}

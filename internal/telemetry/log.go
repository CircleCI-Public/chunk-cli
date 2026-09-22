package telemetry

import (
	"fmt"
	"os"

	"github.com/segmentio/analytics-go/v3"
)

// loggingDestination writes track calls to stderr, gated behind
// CHUNK_TELEMETRY_LOG so contributors can inspect event payloads without
// sending anything to Segment.
type loggingDestination struct{}

func (l *loggingDestination) Close() error { return nil }

func (l *loggingDestination) Enqueue(m analytics.Message) error {
	switch msg := m.(type) {
	case analytics.Track:
		_, err := fmt.Fprintf(os.Stderr, "[telemetry] track %s %v\n", msg.Event, msg.Properties)
		return err
	case analytics.Identify:
		_, err := fmt.Fprintf(os.Stderr, "[telemetry] identify user=%s anonymous=%s\n", msg.UserId, msg.AnonymousId)
		return err
	default:
		_, err := fmt.Fprintf(os.Stderr, "[telemetry] %T\n", m)
		return err
	}
}

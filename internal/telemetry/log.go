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

func (l *loggingDestination) Enqueue(m analytics.Track) error {
	_, err := fmt.Fprintf(os.Stderr, "[telemetry] track %s %v\n", m.Event, m.Properties)
	return err
}

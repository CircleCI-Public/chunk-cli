// Package receiver forwards buffered telemetry events to Segment. It runs
// inside the detached "chunk receive-telemetry" subprocess spawned by
// internal/telemetry's delegateDestination, out of the parent CLI's exit path.
package receiver

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/segmentio/analytics-go/v3"
)

const (
	// EnvWriteKey configures the Segment write key for the delegate subprocess.
	EnvWriteKey = "__CHUNK_TELEMETRY_WRITE_KEY"
	// EnvTelemetryEndpoint configures the Segment endpoint for the delegate subprocess.
	EnvTelemetryEndpoint = "__CHUNK_TELEMETRY_ENDPOINT"
)

// Receive decodes a JSON array of analytics.Track events from in and
// forwards them to Segment using the write key and endpoint from the
// environment.
func Receive(in io.Reader) (err error) {
	writeKey := os.Getenv(EnvWriteKey)
	endpoint := os.Getenv(EnvTelemetryEndpoint)

	var messages []analytics.Track
	if err := json.NewDecoder(in).Decode(&messages); err != nil {
		return err
	}

	if writeKey == "" {
		return errors.New("write key is required")
	}
	c, err := analytics.NewWithConfig(writeKey, analytics.Config{
		Endpoint: endpoint,
	})
	if err != nil {
		return fmt.Errorf("create segment client: %w", err)
	}

	defer func() {
		cerr := c.Close()
		if err == nil {
			err = cerr
		}
	}()

	for _, m := range messages {
		if err := c.Enqueue(m); err != nil {
			return err
		}
	}

	return nil
}

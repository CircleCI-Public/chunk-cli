package fakes

import (
	"context"
	"sync"
	"testing"

	"github.com/segmentio/analytics-go/v3"
	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/telemetry"
)

// Events records the telemetry events a Sender reports, in order.
type Events struct {
	mu     sync.Mutex
	tracks []analytics.Track
}

// Tracking returns ctx with a Sender whose events are recorded.
func Tracking(ctx context.Context, t testing.TB) (context.Context, *Events) {
	t.Helper()
	ev := &Events{}
	s, err := telemetry.NewSender(telemetry.Config{TestDestination: ev})
	assert.NilError(t, err)
	return telemetry.WithSender(ctx, s), ev
}

func (e *Events) Enqueue(m analytics.Message) error {
	if tr, ok := m.(analytics.Track); ok {
		e.mu.Lock()
		e.tracks = append(e.tracks, tr)
		e.mu.Unlock()
	}
	return nil
}

func (e *Events) Close() error { return nil }

// Names returns the name of every recorded event, in order.
func (e *Events) Names() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, 0, len(e.tracks))
	for _, tr := range e.tracks {
		out = append(out, tr.Event)
	}
	return out
}

// Named returns the recorded events called name, in order.
func (e *Events) Named(name string) []analytics.Track {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []analytics.Track
	for _, tr := range e.tracks {
		if tr.Event == name {
			out = append(out, tr)
		}
	}
	return out
}

// Last returns the most recent event. It panics if none was recorded.
func (e *Events) Last() analytics.Track {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.tracks[len(e.tracks)-1]
}

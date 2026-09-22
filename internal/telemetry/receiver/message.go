package receiver

import (
	"encoding/json"
	"fmt"

	"github.com/segmentio/analytics-go/v3"
)

// Message kinds carried on the wire between the CLI and the detached
// receive-telemetry subprocess.
const (
	KindTrack    = "track"
	KindIdentify = "identify"
)

// Message is the wire envelope for one buffered Segment message. Events reach
// the subprocess as a JSON array of these, so a payload can mix command
// invocation tracks with the identify calls that join an anonymous ID to a
// user ID — a bare analytics.Track array could only carry the former.
type Message struct {
	Kind     string              `json:"kind"`
	Track    *analytics.Track    `json:"track,omitempty"`
	Identify *analytics.Identify `json:"identify,omitempty"`
}

// Wrap puts msg in an envelope. It returns an error for message types the
// wire format does not know about, so adding one to Sender without teaching
// the receiver to forward it fails loudly rather than silently dropping
// events.
func Wrap(msg analytics.Message) (Message, error) {
	switch m := msg.(type) {
	case analytics.Track:
		return Message{Kind: KindTrack, Track: &m}, nil
	case analytics.Identify:
		return Message{Kind: KindIdentify, Identify: &m}, nil
	default:
		return Message{}, fmt.Errorf("unsupported telemetry message type %T", msg)
	}
}

// Unwrap returns the analytics message m carries, or nil when the envelope is
// empty or of an unknown kind (an older CLI's payload, say) — the receiver
// skips those rather than failing the whole batch.
func (m Message) Unwrap() analytics.Message {
	switch m.Kind {
	case KindTrack:
		if m.Track != nil {
			return *m.Track
		}
	case KindIdentify:
		if m.Identify != nil {
			return *m.Identify
		}
	}
	return nil
}

// decode reads the JSON payload written by the CLI's delegate destination.
//
// It accepts the legacy bare `[]analytics.Track` form as well as the envelope
// form: the delegate re-execs the binary at os.Executable(), which an upgrade
// may have replaced with a newer chunk between the parent's start and its
// exit, so a new receiver can be handed an old CLI's payload.
func decode(data []byte) ([]analytics.Message, error) {
	var envelopes []Message
	if err := json.Unmarshal(data, &envelopes); err != nil {
		return nil, err
	}

	msgs := make([]analytics.Message, 0, len(envelopes))
	tagged := false
	for _, e := range envelopes {
		if e.Kind != "" {
			tagged = true
		}
		if msg := e.Unwrap(); msg != nil {
			msgs = append(msgs, msg)
		}
	}
	// An untagged payload is either an older CLI's bare track array or an
	// empty batch; anything tagged is in the envelope form, even if every
	// kind in it was unknown and skipped.
	if tagged || len(envelopes) == 0 {
		return msgs, nil
	}

	var tracks []analytics.Track
	if err := json.Unmarshal(data, &tracks); err != nil {
		return nil, err
	}
	for _, t := range tracks {
		msgs = append(msgs, t)
	}
	return msgs, nil
}

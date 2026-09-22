package receiver

import (
	"encoding/json"
	"testing"

	"github.com/segmentio/analytics-go/v3"
	"gotest.tools/v3/assert"
)

func TestWrapUnwrap_RoundTripsTrackAndIdentify(t *testing.T) {
	msgs := []analytics.Message{
		analytics.Track{Event: "command_invocation", AnonymousId: "anon-1"},
		analytics.Identify{UserId: "user-1", AnonymousId: "anon-1"},
	}

	envelopes := make([]Message, 0, len(msgs))
	for _, m := range msgs {
		e, err := Wrap(m)
		assert.NilError(t, err)
		envelopes = append(envelopes, e)
	}

	data, err := json.Marshal(envelopes)
	assert.NilError(t, err)

	got, err := decode(data)
	assert.NilError(t, err)
	assert.Equal(t, len(got), 2)

	track, ok := got[0].(analytics.Track)
	assert.Assert(t, ok)
	assert.Equal(t, track.Event, "command_invocation")

	identify, ok := got[1].(analytics.Identify)
	assert.Assert(t, ok)
	assert.Equal(t, identify.UserId, "user-1")
	assert.Equal(t, identify.AnonymousId, "anon-1")
}

func TestWrap_RejectsUnsupportedMessage(t *testing.T) {
	_, err := Wrap(analytics.Page{Name: "somewhere"})
	assert.ErrorContains(t, err, "unsupported telemetry message type")
}

// A `chunk upgrade` can replace the binary between a CLI run starting and its
// detached delegate spawning, so a new receiver must still understand the
// bare-track payload an older CLI writes.
func TestDecode_AcceptsLegacyTrackArray(t *testing.T) {
	data, err := json.Marshal([]analytics.Track{{Event: "legacy_event"}})
	assert.NilError(t, err)

	got, err := decode(data)
	assert.NilError(t, err)
	assert.Equal(t, len(got), 1)
	track, ok := got[0].(analytics.Track)
	assert.Assert(t, ok)
	assert.Equal(t, track.Event, "legacy_event")
}

func TestDecode_EmptyArray(t *testing.T) {
	got, err := decode([]byte(`[]`))
	assert.NilError(t, err)
	assert.Equal(t, len(got), 0)
}

func TestDecode_SkipsUnknownKinds(t *testing.T) {
	got, err := decode([]byte(`[{"kind":"page"},{"kind":"track","track":{"event":"e"}}]`))
	assert.NilError(t, err)
	assert.Equal(t, len(got), 1)
}

func TestDecode_InvalidJSON(t *testing.T) {
	_, err := decode([]byte(`not json`))
	assert.Assert(t, err != nil)
}

func TestDecode_UnknownKindDoesNotFallBackToTracks(t *testing.T) {
	got, err := decode([]byte(`[{"kind":"page","page":{"name":"somewhere"}}]`))
	assert.NilError(t, err)
	assert.Equal(t, len(got), 0, "an unknown kind should be skipped, not decoded as a track")
}

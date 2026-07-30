package sse_test

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/sse"
)

func collect(t *testing.T, in string, maxFrame int) ([]sse.Frame, string, error) {
	t.Helper()
	var got []sse.Frame
	last, err := sse.Scan(strings.NewReader(in), maxFrame, func(f sse.Frame) error {
		got = append(got, sse.Frame{
			Event:   f.Event,
			ID:      f.ID,
			Data:    append([]byte(nil), f.Data...),
			Comment: f.Comment,
		})
		return nil
	})
	return got, last, err
}

func TestScan(t *testing.T) {
	in := "event: stdout\nid: 4,0\ndata: aGk=\n\n" +
		": ping\n\n" +
		"event: exit\nid: 4,0\ndata: {\"exit_code\":2}\n\n"

	got, last, err := collect(t, in, 1<<20)
	assert.NilError(t, err)
	assert.Equal(t, last, "4,0")
	assert.Equal(t, len(got), 3)
	assert.Equal(t, got[0].Event, "stdout")
	assert.Equal(t, string(got[0].Data), "aGk=")
	assert.Check(t, got[1].Comment)
	assert.Equal(t, got[2].Event, "exit")
}

// The space after the colon is optional. Requiring it silently drops every frame
// from a conformant server that omits it — which reads as "the stream ended
// without a result" rather than as a parse bug.
func TestScanOptionalSpace(t *testing.T) {
	got, _, err := collect(t, "data:x\n\ndata:  y\n\n", 1<<20)
	assert.NilError(t, err)
	assert.Equal(t, string(got[0].Data), "x")
	assert.Equal(t, string(got[1].Data), " y", "only one leading space is stripped")
}

func TestScanCRLF(t *testing.T) {
	got, _, err := collect(t, "event: stdout\r\ndata: aGk=\r\n\r\n", 1<<20)
	assert.NilError(t, err)
	assert.Equal(t, len(got), 1)
	assert.Equal(t, got[0].Event, "stdout")
}

// A frame with no terminating blank line must be dropped. Delivering it would
// turn a truncated stream into a short-but-plausible event.
func TestScanDiscardsPartialFrame(t *testing.T) {
	got, last, err := collect(t, "event: stdout\nid: 2,0\ndata: aGk=\n\nevent: stdout\ndata: dHJ1", 1<<20)
	assert.NilError(t, err)
	assert.Equal(t, len(got), 1)
	assert.Equal(t, last, "2,0")
}

// Frames past bufio.Scanner's 64KiB token limit must work; exceeding the
// explicit cap must be a loud error rather than a silent stop.
func TestScanLargeFrames(t *testing.T) {
	big := strings.Repeat("A", 300<<10)
	got, _, err := collect(t, "event: stdout\ndata: "+big+"\n\n", 1<<20)
	assert.NilError(t, err)
	assert.Equal(t, len(got[0].Data), len(big))

	_, _, err = collect(t, "data: "+strings.Repeat("x", 4096)+"\n\n", 512)
	assert.Check(t, errors.Is(err, sse.ErrFrameTooLarge), "got %v", err)
}

func TestScanUnknownFieldsIgnored(t *testing.T) {
	got, _, err := collect(t, "event: stdout\nfuture: x\ndata: aGk=\n\n", 1<<20)
	assert.NilError(t, err)
	assert.Equal(t, len(got), 1)
	assert.Equal(t, got[0].Event, "stdout")
}

// Arbitrary bytes must survive as base64 payloads, including the ones that break
// naive line-oriented framing.
func TestScanBase64PayloadsRoundTrip(t *testing.T) {
	payloads := [][]byte{
		[]byte("spinner\rredraw\r"),
		{0x1b, '[', '3', '2', 'm', 'o', 'k'},
		{0x00, 0xff, 0xfe},
		[]byte("\xed\xa0\x80"),
	}

	var b strings.Builder
	for _, p := range payloads {
		b.WriteString("event: stdout\ndata: " + base64.StdEncoding.EncodeToString(p) + "\n\n")
	}

	got, _, err := collect(t, b.String(), 1<<20)
	assert.NilError(t, err)
	assert.Equal(t, len(got), len(payloads))
	for i, f := range got {
		raw, decErr := base64.StdEncoding.DecodeString(string(f.Data))
		assert.NilError(t, decErr)
		assert.Equal(t, string(raw), string(payloads[i]))
	}
}

func TestParseCursor(t *testing.T) {
	tests := []struct {
		in             string
		stdout, stderr int64
		wantErr        bool
	}{
		{in: ""},
		{in: "0,0"},
		{in: "4096,12", stdout: 4096, stderr: 12},
		{in: "4096", wantErr: true},
		{in: "a,b", wantErr: true},
		{in: "-1,0", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			out, errOff, err := sse.ParseCursor(tt.in)
			if tt.wantErr {
				assert.Check(t, err != nil)
				return
			}
			assert.NilError(t, err)
			assert.Equal(t, out, tt.stdout)
			assert.Equal(t, errOff, tt.stderr)
		})
	}
}

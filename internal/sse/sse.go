// Package sse reads Server-Sent Events streams.
//
// It is a reader only — chunk consumes SSE, it never serves it. The server side
// of this contract lives in sandbox-provisioner; the two are separate modules,
// so the framing is implemented on both sides rather than shared.
package sse

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// ErrFrameTooLarge is returned when a single line exceeds the configured
// maximum. It is an explicit error rather than a silent stop: truncating a
// stream because a frame was unexpectedly large is a failure that otherwise
// reads as success. This is why bufio.Scanner, whose 64KiB limit fails exactly
// that way, is deliberately not used here.
var ErrFrameTooLarge = errors.New("sse: frame too large")

// Frame is one decoded SSE frame.
type Frame struct {
	// Event is the "event:" field. Empty for an unnamed frame or a comment.
	Event string
	// ID is the "id:" field — an opaque cursor. Never interpret it; store it and
	// echo it back as Last-Event-ID to resume.
	ID string
	// Data is the payload with the single optional space after the colon
	// stripped.
	Data []byte
	// Comment is true for a ":" comment frame, i.e. a heartbeat.
	Comment bool
}

// Scan reads frames from r until EOF, calling fn for each. It returns the last
// non-empty id seen, so an interrupted reader knows where to resume from.
//
// A partial trailing frame — one with no terminating blank line — is discarded
// rather than delivered, so a truncated stream never surfaces as a short but
// plausible-looking event. maxFrame caps a single line.
//
// Terminators may be "\n" or "\r\n". A lone "\r" is not treated as one: no
// producer we read emits it, and payloads are base64 or JSON so they cannot
// contain one. Were it ever to appear it stays inside the payload rather than
// splitting a line, so framing is undisturbed and the payload fails to decode
// loudly instead of corrupting silently.
func Scan(r io.Reader, maxFrame int, fn func(Frame) error) (string, error) {
	br := bufio.NewReaderSize(r, 32<<10)

	var (
		lastID  string
		f       Frame
		hasData bool
		pending bool
	)

	for {
		line, err := readLine(br, maxFrame)
		if err != nil {
			if errors.Is(err, io.EOF) {
				// Any accumulated partial frame is intentionally dropped.
				return lastID, nil
			}
			return lastID, err
		}

		if len(line) == 0 {
			if pending {
				if err := fn(f); err != nil {
					return lastID, err
				}
				if f.ID != "" {
					lastID = f.ID
				}
			}
			f, hasData, pending = Frame{}, false, false
			continue
		}

		if line[0] == ':' {
			// Between frames a comment is a heartbeat worth surfacing; inside
			// one it is just an ignorable line.
			if !pending {
				if err := fn(Frame{Comment: true}); err != nil {
					return lastID, err
				}
			}
			continue
		}

		name, value := splitField(line)
		pending = true

		switch name {
		case "event":
			f.Event = string(value)
		case "id":
			// Per spec an id containing a NUL is ignored rather than applied.
			if !bytes.ContainsRune(value, 0) {
				f.ID = string(value)
			}
		case "data":
			if hasData {
				// Spec-mandated rejoining of multi-line payloads.
				f.Data = append(f.Data, '\n')
				f.Data = append(f.Data, value...)
				continue
			}
			f.Data = value
			hasData = true
		default:
			// Unknown fields are ignored, which keeps the format extensible.
		}
	}
}

// readLine reads one line without its terminator. The result is freshly
// allocated so the caller may retain it across subsequent reads.
func readLine(r *bufio.Reader, limit int) ([]byte, error) {
	var raw []byte
	for {
		part, err := r.ReadSlice('\n')
		if len(raw)+len(part) > limit {
			return nil, ErrFrameTooLarge
		}
		raw = append(raw, part...)
		if err == nil {
			break
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return nil, err
	}

	line := raw[:len(raw)-1] // drop '\n'
	if len(line) > 0 && line[len(line)-1] == '\r' {
		line = line[:len(line)-1]
	}
	return line, nil
}

// splitField splits a line into field name and value, stripping the single
// optional space after the colon. This is why "data:x" and "data: x" are
// equivalent — reading only the spaced form silently drops every frame from a
// conformant server that omits it.
func splitField(line []byte) (string, []byte) {
	i := bytes.IndexByte(line, ':')
	if i < 0 {
		return string(line), nil
	}
	value := line[i+1:]
	if len(value) > 0 && value[0] == ' ' {
		value = value[1:]
	}
	return string(line[:i]), value
}

// ParseCursor splits the opaque per-stream cursor carried in a frame's id.
// An empty string means "from the beginning" and is not an error, so a first
// connection and a resume share one code path.
func ParseCursor(s string) (stdout, stderr int64, err error) {
	if s == "" {
		return 0, 0, nil
	}
	out, errStream, ok := strings.Cut(s, ",")
	if !ok {
		return 0, 0, fmt.Errorf("sse: malformed cursor %q", s)
	}
	stdout, err = strconv.ParseInt(out, 10, 64)
	if err != nil || stdout < 0 {
		return 0, 0, fmt.Errorf("sse: malformed cursor %q", s)
	}
	stderr, err = strconv.ParseInt(errStream, 10, 64)
	if err != nil || stderr < 0 {
		return 0, 0, fmt.Errorf("sse: malformed cursor %q", s)
	}
	return stdout, stderr, nil
}

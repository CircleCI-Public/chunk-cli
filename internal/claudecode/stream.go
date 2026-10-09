package claudecode

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Activity is one thing claude did during a run, for display.
type Activity struct {
	// Tool is the tool used, or "" for text claude wrote.
	Tool string
	// Detail is the tool's target (a file, a command) or the text.
	Detail string
}

// structuredOutputTool is the tool Claude Code answers through when a schema
// is given. It is how the answer arrives, not something claude did.
const structuredOutputTool = "StructuredOutput"

// maxLineBytes caps one buffered stream-json line. A tool result echoing a
// large file can be long; a line past this is dropped rather than buffered.
// It is as large as a structured result may be, since the result event that
// carries one is a single line.
const maxLineBytes = maxStructuredOutputBytes

// streamEvent is the part of one stream-json line a run reads.
type streamEvent struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
	Message   struct {
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
	} `json:"message"`
	IsError      bool    `json:"is_error"`
	Result       string  `json:"result"`
	TotalCostUSD float64 `json:"total_cost_usd"`
}

// StreamResult is the result event that ends a stream-json run.
type StreamResult struct {
	IsError bool
	// Result is claude's final text.
	Result  string
	CostUSD float64
	// Raw is the whole event, the same shape as --output-format json's
	// result, so ParseResult reads it.
	Raw string
}

// Stream reads claude's --output-format stream-json output as it arrives,
// which can be mid-line, and reports what claude does.
type Stream struct {
	onActivity func(Activity)
	buf        []byte
	overflow   bool
	sessionID  string
	result     StreamResult
	sawResult  bool
}

// NewStream returns a Stream that reports each tool call and piece of text to
// onActivity, which may be nil.
func NewStream(onActivity func(Activity)) *Stream {
	return &Stream{onActivity: onActivity}
}

// Write takes the next run of stdout.
func (s *Stream) Write(data []byte) {
	for len(data) > 0 {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			s.appendPartial(data)
			return
		}
		s.appendPartial(data[:i])
		if !s.overflow {
			s.line(s.buf)
		}
		s.buf, s.overflow = s.buf[:0], false
		data = data[i+1:]
	}
}

// Flush handles a last line that had no trailing newline.
func (s *Stream) Flush() {
	if len(s.buf) > 0 && !s.overflow {
		s.line(s.buf)
	}
	s.buf, s.overflow = nil, false
}

// SessionID is the session the run reported, or "" before it has.
func (s *Stream) SessionID() string { return s.sessionID }

// Result is the run's result event, and whether one arrived.
func (s *Stream) Result() (StreamResult, bool) { return s.result, s.sawResult }

func (s *Stream) appendPartial(data []byte) {
	if s.overflow || len(s.buf)+len(data) > maxLineBytes {
		s.overflow = true
		return
	}
	s.buf = append(s.buf, data...)
}

func (s *Stream) line(b []byte) {
	var e streamEvent
	if json.Unmarshal(b, &e) != nil {
		return
	}
	if e.SessionID != "" {
		s.sessionID = e.SessionID
	}
	switch e.Type {
	case "result":
		s.result = StreamResult{IsError: e.IsError, Result: e.Result, CostUSD: e.TotalCostUSD, Raw: string(b)}
		s.sawResult = true
	case "assistant":
		if s.onActivity == nil {
			return
		}
		for _, c := range e.Message.Content {
			switch {
			case c.Type == "text":
				if t := strings.TrimSpace(c.Text); t != "" {
					s.onActivity(Activity{Detail: t})
				}
			case c.Type == "tool_use" && c.Name != structuredOutputTool:
				s.onActivity(Activity{Tool: c.Name, Detail: toolDetail(c.Input)})
			}
		}
	}
}

// toolDetail picks the field of a tool's input that says what it acted on.
func toolDetail(input json.RawMessage) string {
	var in map[string]any
	if json.Unmarshal(input, &in) != nil {
		return ""
	}
	for _, key := range []string{"file_path", "command", "pattern", "path", "description"} {
		if v, ok := in[key].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

package claudecode

import (
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

// Output arrives in chunks that cut lines anywhere, as the exec stream
// delivers it.
func TestStreamReassemblesSplitLines(t *testing.T) {
	var acts []Activity
	s := NewStream(func(a Activity) { acts = append(acts, a) })
	out := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"a.go"}}]}}` + "\n" +
		`{"type":"result","result":"ok","total_cost_usd":0.25,"session_id":"s-1"}`
	for i := 0; i < len(out); i += 7 {
		s.Write([]byte(out[i:min(i+7, len(out))]))
	}
	s.Flush()

	assert.DeepEqual(t, acts, []Activity{{Tool: "Read", Detail: "a.go"}})
	res, ok := s.Result()
	assert.Assert(t, ok)
	assert.Equal(t, res.Result, "ok")
	assert.Equal(t, res.CostUSD, 0.25)
	assert.Equal(t, s.SessionID(), "s-1")
}

// With a schema, Claude Code answers through a StructuredOutput tool call. That
// is the answer arriving, not something claude did.
func TestStreamLeavesOutTheStructuredAnswer(t *testing.T) {
	var acts []Activity
	s := NewStream(func(a Activity) { acts = append(acts, a) })

	s.Write([]byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"StructuredOutput","input":{"greeting":"hi"}},{"type":"text","text":"  Done.  "}]}}` + "\n"))

	assert.DeepEqual(t, acts, []Activity{{Detail: "Done."}})
}

// The result event is kept whole, so ParseResult reads it as it reads
// --output-format json's result. Events after it, which claude does send, do
// not replace it.
func TestStreamKeepsTheRawResult(t *testing.T) {
	s := NewStream(nil)
	line := `{"type":"result","subtype":"success","is_error":false,"result":"done","structured_output":{"a":1}}`

	s.Write([]byte(line + "\n" + `{"type":"system","subtype":"session_state_changed"}` + "\n"))

	res, ok := s.Result()
	assert.Assert(t, ok)
	assert.Equal(t, res.Raw, line)
	parsed, err := ParseResult(res.Raw)
	assert.NilError(t, err)
	assert.Equal(t, string(parsed.StructuredOutput), `{"a":1}`)
}

func TestStreamDropsOversizedLines(t *testing.T) {
	s := NewStream(nil)

	s.Write([]byte(`{"type":"user","x":"` + strings.Repeat("a", maxLineBytes) + `"}` + "\n"))
	s.Write([]byte(`{"type":"result","result":"ok"}` + "\n"))

	_, ok := s.Result()
	assert.Assert(t, ok)
	assert.Assert(t, len(s.buf) < maxLineBytes)
}

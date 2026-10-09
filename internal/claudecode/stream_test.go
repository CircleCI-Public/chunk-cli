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

func TestStreamDropsOversizedLines(t *testing.T) {
	s := NewStream(nil)

	s.Write([]byte(`{"type":"user","x":"` + strings.Repeat("a", maxLineBytes) + `"}` + "\n"))
	s.Write([]byte(`{"type":"result","result":"ok"}` + "\n"))

	_, ok := s.Result()
	assert.Assert(t, ok)
	assert.Assert(t, len(s.buf) < maxLineBytes)
}

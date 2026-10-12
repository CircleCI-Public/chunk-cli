package claude

import (
	"os"
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"

	"github.com/smeltdev/smelt/internal/harness"
)

// TestParserReassemblesSplitLines covers output arriving in chunks that cut
// lines anywhere, as the exec stream delivers it.
func TestParserReassemblesSplitLines(t *testing.T) {
	var acts []harness.Activity
	p := Harness{}.NewImplementParser(func(a harness.Activity) { acts = append(acts, a) })
	stream := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"a.go"}}]}}` + "\n" +
		`{"type":"result","result":"ok"}`
	for i := 0; i < len(stream); i += 7 {
		p.Write([]byte(stream[i:min(i+7, len(stream))]))
	}
	p.Flush()
	assert.DeepEqual(t, acts, []harness.Activity{{Tool: "Read", Detail: "a.go"}})
	assert.Assert(t, p.Outcome().Done)
	assert.Equal(t, p.Outcome().Turn.Summary, "ok")
}

func TestParserDropsOversizedLines(t *testing.T) {
	p := Harness{}.NewImplementParser(nil)
	p.Write([]byte(`{"type":"user","x":"` + strings.Repeat("a", harness.MaxLineBytes) + `"}` + "\n"))
	p.Write([]byte(`{"type":"result","result":"ok"}` + "\n"))
	assert.Assert(t, p.Outcome().Done)
}

func TestParserReadsTheRealStream(t *testing.T) {
	b, err := os.ReadFile("../testdata/real/claude-stream.jsonl")
	assert.NilError(t, err)
	p := Harness{}.NewImplementParser(nil)
	p.Write(b)
	p.Flush()
	out := p.Outcome()
	assert.Assert(t, out.Done && !out.Failed)
	assert.Equal(t, out.Turn.Summary, "ok")
	assert.Equal(t, out.Turn.OutputTokens, 4)
	assert.Equal(t, out.Turn.CachedTokens, 15333)
	assert.Equal(t, out.Turn.InputTokens, 2796+2074+15333)
	assert.Assert(t, out.Turn.CostUSD > 0)
	assert.Equal(t, out.Turn.SessionID, "00000000-0000-4000-8000-000000000004")
	rl := out.Turn.RateLimit
	assert.Assert(t, rl != nil)
	assert.Equal(t, rl.Status, "allowed")
	assert.Equal(t, rl.Bucket, "5h")
	assert.Assert(t, rl.ResetsAt.Equal(time.Unix(1791764400, 0)))
}

func TestParserKeepsARejectedRateLimit(t *testing.T) {
	p := Harness{}.NewImplementParser(nil)
	p.Write([]byte(`{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","rateLimitType":"seven_day"}}` + "\n"))
	rl := p.Outcome().Turn.RateLimit
	assert.Assert(t, rl != nil)
	assert.Equal(t, rl.Status, "rejected")
	assert.Equal(t, rl.Bucket, "7d")
	assert.Assert(t, rl.ResetsAt == nil)
}

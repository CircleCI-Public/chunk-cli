package claudecode

import (
	"testing"

	"gotest.tools/v3/assert"
)

func TestEvents(t *testing.T) {
	for _, tc := range []struct {
		name string
		line string
		want []Event
	}{
		{
			name: "init",
			line: `{"type":"system","subtype":"init","session_id":"s1","plugins":[{"name":"f"}],"mcp_servers":[{"name":"plugin:f:factory","status":"connected"}]}`,
			want: []Event{{Kind: KindInit, Init: Init{
				SessionID: "s1", Plugins: []string{"f"}, MCPServers: map[string]string{"plugin:f:factory": "connected"},
			}}},
		},
		{
			name: "assistant text and tool_use, in order",
			line: `{"type":"assistant","message":{"content":[` +
				`{"type":"text","text":" hello "},` +
				`{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"a.go"}}` +
				`]}}`,
			want: []Event{
				{Kind: KindActivity, Activity: Activity{Detail: "hello"}},
				{Kind: KindActivity, Activity: Activity{Tool: "Read", Detail: "a.go"}},
			},
		},
		{
			name: "structured output tool_use produces no activity",
			line: `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"StructuredOutput","input":{"a":1}}]}}`,
			want: nil,
		},
		{
			name: "task_notification",
			line: `{"type":"system","subtype":"task_notification","task_id":"b1","tool_use_id":"t1","status":"completed",` +
				`"output_file":"/tmp/b1.output","summary":"Background command \"wait\" completed"}`,
			want: []Event{{Kind: KindTaskEnded, TaskID: "b1"}},
		},
		{
			name: "result",
			line: `{"type":"result","subtype":"success","result":"reply","total_cost_usd":0.04}`,
			want: []Event{{Kind: KindTurnEnded, Result: StreamResult{
				Result: "reply", CostUSD: 0.04,
				Raw: `{"type":"result","subtype":"success","result":"reply","total_cost_usd":0.04}`,
			}}},
		},
		{
			name: "other system lines produce nothing",
			line: `{"type":"system","subtype":"task_started","task_id":"b1"}`,
			want: nil,
		},
		{
			name: "unknown type produces nothing",
			line: `{"type":"something_else"}`,
			want: nil,
		},
		{
			name: "non-JSON line produces nothing",
			line: `not json at all`,
			want: nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []Event
			e := NewEvents(func(ev Event) { got = append(got, ev) })
			e.Write([]byte(tc.line + "\n"))
			assert.DeepEqual(t, got, tc.want)
		})
	}
}

// Output can arrive in chunks that cut a line anywhere, as the exec stream
// delivers it.
func TestEventsReassemblesSplitLines(t *testing.T) {
	var got []Event
	e := NewEvents(func(ev Event) { got = append(got, ev) })
	out := `{"type":"system","subtype":"task_notification","task_id":"b1"}` + "\n" +
		`{"type":"result","result":"ok","total_cost_usd":0.25}`
	for i := 0; i < len(out); i += 7 {
		e.Write([]byte(out[i:min(i+7, len(out))]))
	}
	e.Flush()

	assert.DeepEqual(t, got, []Event{
		{Kind: KindTaskEnded, TaskID: "b1"},
		{Kind: KindTurnEnded, Result: StreamResult{Result: "ok", CostUSD: 0.25, Raw: `{"type":"result","result":"ok","total_cost_usd":0.25}`}},
	})
}

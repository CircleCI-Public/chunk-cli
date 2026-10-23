package claudecode

import (
	"encoding/json"
	"strings"
)

// EventKind is what kind of thing an Event reports.
type EventKind int

const (
	// KindInit is a system/init line. claude sends one once the first message
	// arrives, and again on every turn.
	KindInit EventKind = iota
	// KindActivity is a piece of text claude wrote, or a tool call that is not
	// the structured-output tool.
	KindActivity
	// KindTaskEnded is a background task ending, whose notification starts a
	// turn of its own.
	KindTaskEnded
	// KindTurnEnded is the result event that ends a turn.
	KindTurnEnded
)

// Init is what Claude Code reported about itself at the start of a turn.
type Init struct {
	SessionID string
	Plugins   []string
	// MCPServers maps a server's name to its status, such as "connected".
	MCPServers map[string]string
}

// Event is one thing that happened in a long-lived Claude Code session. Which
// fields are set depends on Kind.
type Event struct {
	Kind EventKind

	// Init is set for KindInit.
	Init Init

	// Activity is set for KindActivity.
	Activity Activity

	// TaskID is set for KindTaskEnded.
	TaskID string

	// Result is set for KindTurnEnded.
	Result StreamResult
}

// Events reads a long-lived claude session's --output-format stream-json
// output as it arrives, which can be mid-line, and reports each Event to on.
type Events struct {
	on    func(Event)
	lines lineSplitter
}

// NewEvents returns an Events that reports each event to on.
func NewEvents(on func(Event)) *Events {
	return &Events{on: on}
}

// Write takes the next run of stdout.
func (e *Events) Write(data []byte) { e.lines.write(data, e.line) }

// Flush handles a last line that had no trailing newline.
func (e *Events) Flush() { e.lines.flush(e.line) }

// rawEvent is the part of one stream-json line Events reads.
type rawEvent struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	SessionID string `json:"session_id"`
	TaskID    string `json:"task_id"`
	Plugins   []struct {
		Name string `json:"name"`
	} `json:"plugins"`
	MCPServers []struct {
		Name   string `json:"name"`
		Status string `json:"status"`
	} `json:"mcp_servers"`
	Message struct {
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

func (e *Events) line(b []byte) {
	var raw rawEvent
	if json.Unmarshal(b, &raw) != nil {
		return
	}
	switch raw.Type {
	case "system":
		e.system(raw)
	case "assistant":
		e.assistant(raw)
	case "result":
		e.on(Event{Kind: KindTurnEnded, Result: StreamResult{
			IsError: raw.IsError, Result: raw.Result, CostUSD: raw.TotalCostUSD, Raw: string(b),
		}})
	}
}

func (e *Events) system(raw rawEvent) {
	switch raw.Subtype {
	case "init":
		plugins := make([]string, 0, len(raw.Plugins))
		for _, p := range raw.Plugins {
			plugins = append(plugins, p.Name)
		}
		mcpServers := make(map[string]string, len(raw.MCPServers))
		for _, m := range raw.MCPServers {
			mcpServers[m.Name] = m.Status
		}
		e.on(Event{Kind: KindInit, Init: Init{SessionID: raw.SessionID, Plugins: plugins, MCPServers: mcpServers}})
	case "task_notification":
		e.on(Event{Kind: KindTaskEnded, TaskID: raw.TaskID})
	}
}

func (e *Events) assistant(raw rawEvent) {
	for _, c := range raw.Message.Content {
		switch c.Type {
		case contentTypeText:
			if t := strings.TrimSpace(c.Text); t != "" {
				e.on(Event{Kind: KindActivity, Activity: Activity{Detail: t}})
			}
		case "tool_use":
			if c.Name != structuredOutputTool {
				e.on(Event{Kind: KindActivity, Activity: Activity{
					Tool: c.Name, Detail: toolDetail(c.Input), Description: toolDescription(c.Input),
				}})
			}
		}
	}
}

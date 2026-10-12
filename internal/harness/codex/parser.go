package codex

import (
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"github.com/smeltdev/smelt/internal/harness"
)

// event is the part of one codex exec JSONL line the implementer reads.
type event struct {
	Type     string `json:"type"`
	ThreadID string `json:"thread_id"`
	Message  string `json:"message"`
	Item     struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Command string `json:"command"`
		Changes []struct {
			Path string `json:"path"`
		} `json:"changes"`
	} `json:"item"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		CachedTokens int `json:"cached_input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

type parser struct {
	harness.Lines
	onActivity func(harness.Activity)
	turn       harness.Turn
	done       bool
	failed     bool
	failure    string
}

// NewImplementParser implements harness.Harness.
func (Harness) NewImplementParser(onActivity func(harness.Activity)) harness.StreamParser {
	p := &parser{onActivity: onActivity}
	p.Handle = p.line
	return p
}

func (p *parser) line(b []byte) {
	var e event
	if json.Unmarshal(b, &e) != nil {
		return
	}
	switch e.Type {
	case "thread.started":
		p.turn.SessionID = e.ThreadID
	case "item.completed":
		p.item(e)
	case "turn.completed":
		p.done = true
		p.turn.InputTokens = e.Usage.InputTokens
		p.turn.CachedTokens = e.Usage.CachedTokens
		p.turn.OutputTokens = e.Usage.OutputTokens
	case "error":
		p.fail(e.Message)
	case "turn.failed":
		p.done = true
		p.fail(e.Error.Message)
	}
}

func (p *parser) item(e event) {
	switch e.Item.Type {
	case "agent_message":
		t := strings.TrimSpace(e.Item.Text)
		p.turn.Summary = t
		p.activity(harness.Activity{Detail: t})
	case "command_execution":
		p.activity(harness.Activity{Tool: "Bash", Detail: e.Item.Command})
	case "file_change":
		paths := make([]string, 0, len(e.Item.Changes))
		for _, c := range e.Item.Changes {
			paths = append(paths, c.Path)
		}
		p.activity(harness.Activity{Tool: "Edit", Detail: strings.Join(paths, ", ")})
	case "reasoning":
		p.activity(harness.Activity{Tool: "Reasoning", Detail: strings.TrimSpace(e.Item.Text)})
	}
}

func (p *parser) activity(a harness.Activity) {
	if p.onActivity != nil && (a.Tool != "" || a.Detail != "") {
		p.onActivity(a)
	}
}

// fail records why the turn failed. Codex wraps the API's error in a JSON
// string, so the readable message is dug out of it. The first failure wins:
// codex reports the same one as an error event and again as turn.failed.
func (p *parser) fail(raw string) {
	p.failed = true
	if p.failure != "" {
		return
	}
	p.failure = readableError(raw)
	if usageLimitRe.MatchString(p.failure) {
		p.turn.RateLimit = &harness.RateLimit{Status: "limited", ResetsAt: p.resetTime(p.failure)}
	}
}

// readableError unwraps {"error":{"message":…}} when raw is that.
func readableError(raw string) string {
	var wrapped struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal([]byte(raw), &wrapped) != nil {
		return strings.TrimSpace(raw)
	}
	if m := strings.TrimSpace(wrapped.Error.Message); m != "" {
		return m
	}
	if m := strings.TrimSpace(wrapped.Message); m != "" {
		return m
	}
	return strings.TrimSpace(raw)
}

var (
	usageLimitRe = regexp.MustCompile(`(?i)usage limit|rate limit|try again`)
	// resetRe finds the time codex says the limit lifts, e.g. "try again at
	// 2026-10-11T14:05:00Z" or "try again at Oct 11th, 2026 2:05 PM".
	resetRe = regexp.MustCompile(`(?i)try again (?:at|after)\s+([^.\n]+)`)
	ordinal = regexp.MustCompile(`(\d)(st|nd|rd|th)\b`)
)

// resetLayouts are the time formats codex has been seen to print.
var resetLayouts = []string{time.RFC3339, "Jan 2, 2006 3:04 PM", "Jan 2, 2006 15:04", "2006-01-02 15:04"}

// resetTime reads the reset time out of a usage-limit message, or nil when it
// has none that parses.
func (p *parser) resetTime(msg string) *time.Time {
	m := resetRe.FindStringSubmatch(msg)
	if m == nil {
		return nil
	}
	s := ordinal.ReplaceAllString(strings.TrimSpace(m[1]), "$1")
	for _, layout := range resetLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			t = t.UTC()
			return &t
		}
	}
	return nil
}

// Outcome implements harness.StreamParser.
func (p *parser) Outcome() harness.Outcome {
	return harness.Outcome{Turn: p.turn, Done: p.done, Failed: p.failed, Message: p.failure}
}

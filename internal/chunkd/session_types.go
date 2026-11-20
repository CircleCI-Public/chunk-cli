package chunkd

// ChatMessage is one message to a repository's chat.
type ChatMessage struct {
	Root string `json:"root"`
	// Text is the developer's words, as the conversation shows them.
	Text string `json:"text"`
	// Context is what the screen it was sent from says, sent to claude
	// before Text; "" for none.
	Context string `json:"context,omitempty"`
}

// SessionLog is a daemon-run session's recent events, after a cursor.
type SessionLog struct {
	SessionID string `json:"session_id"`
	// LogID names this run of the log; a client whose cursor came from
	// another (the daemon restarted) starts again at Oldest.
	LogID string `json:"log_id"`
	// Oldest is the first seq still kept; events before it were dropped.
	Oldest int `json:"oldest"`
	// Busy is whether the session is mid-turn, and Activity the turn's last
	// activity line, so a screen opened mid-turn can show it working.
	Busy     bool              `json:"busy"`
	Activity string            `json:"activity,omitempty"`
	Next     int               `json:"next"`
	Events   []SessionLogEvent `json:"events"`
}

// SessionLogKind is what one event in a SessionLog is.
type SessionLogKind string

// Session log event kinds.
const (
	SessionLogActivity SessionLogKind = "activity"
	SessionLogReply    SessionLogKind = "reply"
	SessionLogProblem  SessionLogKind = "problem"
	// SessionLogMessage is the developer's words to a chat, as they
	// typed them, without the context sent with them.
	SessionLogMessage SessionLogKind = "message"
)

// SessionLogEvent is one event in a SessionLog.
type SessionLogEvent struct {
	Seq    int            `json:"seq"`
	Kind   SessionLogKind `json:"kind"`
	Text   string         `json:"text"`
	ByTask bool           `json:"by_task,omitempty"`
}

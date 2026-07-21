package telemetry

import "os"

// Coding agent env vars and the stable agent names they map to.
const (
	envClaudeCode = "CLAUDECODE" // set by Claude Code in every shell it spawns
	envCursor     = "CURSOR_TRACE_ID"

	agentClaudeCode = "claude-code"
	agentCursor     = "cursor"
)

// agentEnvSignals returns the well-known environment variables that AI coding
// agents set in the shells they spawn, paired with their stable agent names.
// The list is intentionally conservative — only signals confirmed to be set by
// the agent itself are included, to avoid misattributing invocations. Extend it
// as other agents' env vars are confirmed.
func agentEnvSignals() []struct{ env, agent string } {
	return []struct{ env, agent string }{
		{envClaudeCode, agentClaudeCode},
		{envCursor, agentCursor},
	}
}

// DetectCodingAgent returns a stable name for the AI coding agent chunk-cli
// appears to have been invoked from, based on well-known environment
// variables those agents set in their shells. Returns "" if no known agent
// is detected, which is the common case for a human running chunk directly.
func DetectCodingAgent() string {
	for _, sig := range agentEnvSignals() {
		if os.Getenv(sig.env) != "" {
			return sig.agent
		}
	}
	return ""
}

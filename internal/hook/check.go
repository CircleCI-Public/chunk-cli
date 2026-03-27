package hook

import (
	"fmt"
	"os"
	"strings"
)

// CheckResult is the outcome of evaluating a sentinel.
type CheckResult struct {
	Kind     string // "missing", "pending", "pass", "fail"
	Sentinel *SentinelData
}

// evaluateSentinel evaluates a sentinel with session and content staleness checks.
func evaluateSentinel(sentinel *SentinelData, currentSessionID, currentContentHash string) CheckResult {
	if sentinel == nil {
		return CheckResult{Kind: "missing"}
	}

	// Session-aware staleness
	if currentSessionID != "" {
		if sentinel.SessionID == "" || sentinel.SessionID != currentSessionID {
			return CheckResult{Kind: "missing"}
		}
	}

	// Pending sentinels don't carry content hashes
	if sentinel.Status == "pending" {
		return CheckResult{Kind: "pending", Sentinel: sentinel}
	}

	// Content-aware staleness
	if currentContentHash != "" {
		if sentinel.ContentHash == "" || sentinel.ContentHash != currentContentHash {
			return CheckResult{Kind: "missing"}
		}
	}

	if sentinel.Status == "pass" {
		return CheckResult{Kind: "pass", Sentinel: sentinel}
	}
	return CheckResult{Kind: "fail", Sentinel: sentinel}
}

// ExecCheckVerdict is the outcome of pre-evaluating an exec spec.
type ExecCheckVerdict struct {
	Kind     string // "skip-trigger", "skip-no-changes", "missing", "pending", "pass", "fail"
	Sentinel *SentinelData
}

// preEvaluateExec runs the full check pipeline for an exec spec.
func preEvaluateExec(cfg *ResolvedConfig, event map[string]interface{}, exec ExecConfig, name string, staged bool, on, trigger string, hasChangesCache *bool, contentHashCache string) ExecCheckVerdict {
	// Trigger matching
	patterns := resolveTriggerPatterns(cfg, on, trigger)
	if len(patterns) > 0 && !matchesTrigger(event, patterns) {
		return ExecCheckVerdict{Kind: "skip-trigger"}
	}

	// Skip if no changes
	if !exec.Always {
		var hasChanges bool
		if hasChangesCache != nil {
			hasChanges = *hasChangesCache
		} else {
			var err error
			hasChanges, err = detectChanges(cfg.ProjectDir, exec.FileExt, staged)
			if err != nil {
				hasChanges = false
			}
		}
		if !hasChanges {
			return ExecCheckVerdict{Kind: "skip-no-changes"}
		}
	}

	// Read sentinel
	sentinel := readSentinel(cfg.SentinelDir, cfg.ProjectDir, name)

	// Session-aware staleness
	marker := ReadMarker(cfg.ProjectDir)
	currentSessionID := ""
	if marker != nil {
		currentSessionID = marker.SessionID
	}

	// Content-aware staleness
	contentHash := contentHashCache
	if contentHash == "" {
		contentHash = computeFingerprint(cfg.ProjectDir, staged, exec.FileExt)
	}

	result := evaluateSentinel(sentinel, currentSessionID, contentHash)

	// Command validation: prevent --cmd bypass
	if (result.Kind == verdictPass || result.Kind == verdictFail) && sentinel != nil {
		sentinelCmd := sentinel.ConfiguredCommand
		if sentinelCmd == "" {
			sentinelCmd = sentinel.Command
		}
		if sentinelCmd != "" && sentinelCmd != exec.Command {
			return ExecCheckVerdict{Kind: "missing"}
		}
	}

	return ExecCheckVerdict(result)
}

// resolveTriggerPatterns resolves trigger patterns from --on or --trigger flags.
func resolveTriggerPatterns(cfg *ResolvedConfig, on, trigger string) []string {
	if trigger != "" {
		return []string{trigger}
	}
	if on != "" {
		patterns, ok := cfg.Triggers[on]
		if !ok {
			return nil
		}
		return patterns
	}
	return nil
}

// matchesTrigger checks whether the event matches any trigger patterns.
// Patterns are matched as case-insensitive substrings against the shell command.
func matchesTrigger(event map[string]interface{}, patterns []string) bool {
	if len(patterns) == 0 {
		return true
	}

	command := extractShellCommand(event)
	if command == "" {
		return false
	}

	lower := strings.ToLower(command)
	for _, p := range patterns {
		if strings.Contains(lower, strings.ToLower(p)) {
			return true
		}
	}
	return false
}

// extractShellCommand extracts the shell command from a tool_input payload.
func extractShellCommand(event map[string]interface{}) string {
	toolInput, ok := event["tool_input"].(map[string]interface{})
	if !ok {
		return ""
	}
	cmd, _ := toolInput["command"].(string)
	return cmd
}

// Response represents the action to take: allow or block.
type Response struct {
	Action  string // "allow" or "block"
	Message string // block reason (only for "block")
}

// blockWithLimit blocks or auto-allows if limit exceeded.
// Returns the response to emit.
func blockWithLimit(cfg *ResolvedConfig, name string, limit int, reason string) Response {
	count := incrementBlockCount(cfg.SentinelDir, cfg.ProjectDir, name)
	if limit > 0 && count > limit {
		resetBlockCount(cfg.SentinelDir, cfg.ProjectDir, name)
		return Response{Action: "allow"}
	}
	return Response{
		Action:  "block",
		Message: withProjectHeader(cfg.ProjectDir, reason),
	}
}

// blockNoCount blocks without incrementing the counter.
func blockNoCount(projectDir, reason string) Response {
	return Response{
		Action:  "block",
		Message: withProjectHeader(projectDir, reason),
	}
}

func withProjectHeader(projectDir, reason string) string {
	return fmt.Sprintf("[project: %s]\n%s", projectDir, reason)
}

// guardStopEvent guards against infinite recursion on Stop events.
// Returns an allow response if recursion should be stopped, nil otherwise.
func guardStopEvent(event map[string]interface{}, limit int) *Response {
	if !isStopRecursion(event) {
		return nil
	}
	if limit > 0 {
		// Let blockWithLimit handle it
		return nil
	}
	resp := Response{Action: "allow"}
	return &resp
}

func isStopRecursion(event map[string]interface{}) bool {
	// Claude Code sets stop_hook_active=true when re-firing Stop after a block
	active, _ := event["stop_hook_active"].(bool)
	return active
}

// emitResponse writes the hook response to stderr (for block) and exits.
func emitResponse(resp Response) error {
	if resp.Action == "allow" {
		return nil // exit 0
	}
	// Write block reason to stderr and return an error that signals exit 2
	fmt.Fprint(os.Stderr, resp.Message)
	return &BlockError{Message: resp.Message}
}

// BlockError signals that the hook should exit with code 2.
type BlockError struct {
	Message string
}

func (e *BlockError) Error() string {
	return e.Message
}

// getBaselineFingerprint retrieves the baseline fingerprint from state.
func getBaselineFingerprint(sentinelDir, projectDir, eventName string) string {
	state := readState(sentinelDir, projectDir)
	eventData, ok := state[eventName].(map[string]interface{})
	if !ok {
		return ""
	}
	entries, ok := eventData["__entries"].([]interface{})
	if !ok || len(entries) == 0 {
		return ""
	}
	last, ok := entries[len(entries)-1].(map[string]interface{})
	if !ok {
		return ""
	}
	fp, _ := last["fingerprint"].(string)
	return fp
}

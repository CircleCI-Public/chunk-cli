package hook

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// TaskCheckFlags holds parsed flags for task check.
type TaskCheckFlags struct {
	Name         string
	Instructions string
	Schema       string
	Always       bool
	Staged       bool
	On           string
	Trigger      string
	Matcher      string
	Limit        int
}

// resolveTask merges flags with config to produce effective task settings.
func resolveTask(cfg *ResolvedConfig, flags TaskCheckFlags) TaskConfig {
	yamlTask, ok := cfg.Tasks[flags.Name]
	if !ok {
		yamlTask = TaskConfig{Limit: 3, Timeout: 600}
	}

	instructions := flags.Instructions
	if instructions == "" {
		instructions = yamlTask.Instructions
	}

	schema := flags.Schema
	if schema == "" {
		schema = yamlTask.Schema
	}

	limit := flags.Limit
	if limit == 0 {
		limit = yamlTask.Limit
	}
	if limit == 0 {
		limit = 3
	}

	always := flags.Always || yamlTask.Always

	timeout := yamlTask.Timeout
	if timeout == 0 {
		timeout = 600
	}

	return TaskConfig{
		Instructions: instructions,
		Schema:       schema,
		Limit:        limit,
		Always:       always,
		Timeout:      timeout,
	}
}

// RunTaskCheck checks a task result. When not enabled, exits 0.
func RunTaskCheck(cfg *ResolvedConfig, flags TaskCheckFlags, event map[string]interface{}) error {
	if !IsEnabled(flags.Name) {
		return nil
	}

	task := resolveTask(cfg, flags)
	limit := task.Limit

	// Stop-event recursion guard
	if resp := guardStopEvent(event, limit); resp != nil {
		return emitResponse(*resp)
	}

	// Trigger matching
	patterns := resolveTriggerPatterns(cfg, flags.On, flags.Trigger)
	if len(patterns) > 0 && !matchesTrigger(event, patterns) {
		return nil // allow
	}

	// Skip if no changes (unless always)
	if !task.Always {
		noChanges := precomputeTaskNoChanges(cfg)
		if noChanges {
			return nil // allow
		}
	}

	// Session-aware staleness
	marker := ReadMarker(cfg.ProjectDir)
	currentSessionID := ""
	if marker != nil {
		currentSessionID = marker.SessionID
	}

	sentinel := readTaskResult(cfg.SentinelDir, cfg.ProjectDir, flags.Name, currentSessionID)
	result := evaluateSentinel(sentinel, currentSessionID, "")

	switch result.Kind {
	case "missing":
		reason := buildTaskCheckBlockMessage(cfg, event, flags, task)
		return emitResponse(blockNoCount(cfg.ProjectDir, reason))

	case "pending":
		timeout := task.Timeout
		if sentinel != nil && sentinel.StartedAt != "" && timeout > 0 {
			started, err := time.Parse(time.RFC3339, sentinel.StartedAt)
			if err == nil {
				elapsed := time.Since(started).Seconds()
				if elapsed > float64(timeout) {
					removeSentinel(cfg.SentinelDir, cfg.ProjectDir, flags.Name)
					reason := fmt.Sprintf(
						"Task %q timed out after %ds (configured timeout: %ds).\n\n"+
							"The previous subagent may have stalled. Re-run the task.",
						flags.Name, int(math.Round(elapsed)), timeout)
					return emitResponse(blockWithLimit(cfg, flags.Name, limit, reason))
				}
			}
		}
		reason := fmt.Sprintf("Task %q is still running. Wait for completion before retrying.", flags.Name)
		return emitResponse(blockNoCount(cfg.ProjectDir, reason))

	case "pass":
		resetBlockCount(cfg.SentinelDir, cfg.ProjectDir, flags.Name)
		return nil // allow

	case "fail":
		reason := "(no reason provided)"
		if result.Sentinel != nil && result.Sentinel.Details != "" {
			reason = result.Sentinel.Details
		}
		agentDetails := reason
		if result.Sentinel != nil && result.Sentinel.RawResult != "" {
			agentDetails = result.Sentinel.RawResult
		}
		msg := fmt.Sprintf("Task blocked: issues found. Fix them before stopping.\n\n%s", agentDetails)
		return emitResponse(blockWithLimit(cfg, flags.Name, limit, msg))
	}

	return nil
}

// readTaskResult reads a task result file and translates to SentinelData.
// Handles both raw SentinelData format and TaskResult format ({decision, reason}).
func readTaskResult(sentinelDir, projectDir, name, sessionID string) *SentinelData {
	path := SentinelPath(sentinelDir, projectDir, name)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil
	}

	// Already in SentinelData format
	if _, ok := raw["status"]; ok {
		if _, ok := raw["startedAt"]; ok {
			var s SentinelData
			if err := json.Unmarshal(data, &s); err != nil {
				return nil
			}
			return &s
		}
	}

	// TaskResult format: {decision: "allow"|"block", reason: "..."}
	decision, _ := raw["decision"].(string)
	if decision != "allow" && decision != "block" {
		return nil
	}

	reason, _ := raw["reason"].(string)
	if reason == "" {
		reason = "(no reason provided)"
	}

	status := "pass"
	exitCode := 0
	if decision == "block" {
		status = "fail"
		exitCode = 1
	}

	return &SentinelData{
		Status:    status,
		StartedAt: time.Now().UTC().Format(time.RFC3339),
		ExitCode:  exitCode,
		Details:   reason,
		RawResult: string(data),
		SessionID: sessionID,
	}
}

// precomputeTaskNoChanges checks whether any code changes occurred since baseline.
func precomputeTaskNoChanges(cfg *ResolvedConfig) bool {
	baselineFP := getBaselineFingerprint(cfg.SentinelDir, cfg.ProjectDir, "UserPromptSubmit")
	if baselineFP == "" {
		return false
	}

	currentFP := computeFingerprint(cfg.ProjectDir, false, "")
	if currentFP == "" || currentFP != baselineFP {
		return false
	}

	return true
}

func buildTaskCheckBlockMessage(cfg *ResolvedConfig, event map[string]interface{}, flags TaskCheckFlags, task TaskConfig) string {
	resultPath := SentinelPath(cfg.SentinelDir, cfg.ProjectDir, flags.Name)

	// Load instructions
	instructions := loadTaskInstructions(task.Instructions, cfg.ProjectDir)

	// Resolve schema
	schema := resolveTaskSchemaContent(cfg.ProjectDir, task.Schema)

	var parts []string
	parts = append(parts, "Spawn a subagent to perform the following task on the current changes.")

	if instructions != "" {
		parts = append(parts, fmt.Sprintf("Instructions:\n\n%s", instructions))
	} else {
		parts = append(parts, "Review the current git diff for correctness, style, and potential issues.")
	}

	parts = append(parts, fmt.Sprintf(
		"Output format:\n\n"+
			"Write the result as a single JSON object to: %s\n"+
			"Schema:\n%s\n\n"+
			"Write ONLY the JSON object — no markdown fences or surrounding text.",
		resultPath, schema))

	parts = append(parts, "Retry after the subagent completes.")

	return strings.Join(parts, "\n\n")
}

// loadTaskInstructions reads and returns task instructions from a file.
func loadTaskInstructions(instructionsPath, projectDir string) string {
	if instructionsPath == "" {
		return ""
	}

	resolved := instructionsPath
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(projectDir, resolved)
	}

	data, err := os.ReadFile(resolved)
	if err != nil {
		return ""
	}
	return string(data)
}

// resolveTaskSchemaContent resolves the task result schema content.
func resolveTaskSchemaContent(projectDir, schemaRaw string) string {
	if schemaRaw == "" {
		return defaultTaskSchema
	}
	schemaPath := schemaRaw
	if !filepath.IsAbs(schemaPath) {
		schemaPath = filepath.Join(projectDir, schemaPath)
	}
	data, err := os.ReadFile(schemaPath)
	if err != nil {
		return defaultTaskSchema
	}
	return strings.TrimSpace(string(data))
}

const defaultTaskSchema = `{
  "type": "object",
  "required": ["decision"],
  "properties": {
    "decision": {
      "enum": ["allow", "block"],
      "description": "allow if no issues found, block if issues require fixing"
    },
    "reason": {
      "type": "string",
      "description": "Short summary of the task outcome"
    },
    "issues": {
      "type": "array",
      "maxItems": 5,
      "items": {
        "type": "object",
        "required": ["severity", "message"],
        "properties": {
          "severity": { "enum": ["CRITICAL", "HIGH"] },
          "file": { "type": "string", "description": "path:line" },
          "message": { "type": "string", "description": "What is wrong (1-2 sentences)" }
        }
      }
    }
  }
}`

package hook

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CommandSpec is a parsed spec like "exec:tests" or "task:review".
type CommandSpec struct {
	Type string // "exec" or "task"
	Name string
}

// ParseSpecs parses command specifiers from positional arguments.
func ParseSpecs(args []string) ([]CommandSpec, error) {
	var specs []CommandSpec
	for _, arg := range args {
		idx := strings.Index(arg, ":")
		if idx == -1 {
			return nil, fmt.Errorf("invalid spec %q: expected type:name", arg)
		}
		typ := arg[:idx]
		name := arg[idx+1:]
		if name == "" {
			return nil, fmt.Errorf("invalid spec %q: empty name", arg)
		}
		if typ != "exec" && typ != "task" {
			return nil, fmt.Errorf("invalid spec type %q: must be exec or task", typ)
		}
		specs = append(specs, CommandSpec{Type: typ, Name: name})
	}
	if len(specs) == 0 {
		return nil, fmt.Errorf("no specs provided")
	}
	return specs, nil
}

// SyncCheckFlags holds parsed flags for sync check.
type SyncCheckFlags struct {
	Specs   []CommandSpec
	On      string
	Trigger string
	Matcher string
	Limit   int
	Staged  bool
	Always  bool
	OnFail  string
	Bail    bool
}

// groupSentinel tracks which commands have passed in a group.
type groupSentinel struct {
	Passed []string `json:"passed"`
}

func groupID(projectDir string, specs []CommandSpec) string {
	var keys []string
	for _, s := range specs {
		keys = append(keys, s.Type+":"+s.Name)
	}
	key := strings.Join(keys, ",")
	h := sha256.Sum256([]byte(projectDir + ":sync:" + key))
	return fmt.Sprintf("sync-%x", h[:8])
}

func groupSentinelPath(sentinelDir, projectDir string, specs []CommandSpec) string {
	return filepath.Join(sentinelDir, groupID(projectDir, specs)+".json")
}

func readGroupSentinel(sentinelDir, projectDir string, specs []CommandSpec) *groupSentinel {
	path := groupSentinelPath(sentinelDir, projectDir, specs)
	data, err := os.ReadFile(path)
	if err != nil {
		return &groupSentinel{}
	}
	var gs groupSentinel
	if err := json.Unmarshal(data, &gs); err != nil {
		return &groupSentinel{}
	}
	return &gs
}

func writeGroupSentinel(sentinelDir, projectDir string, specs []CommandSpec, gs *groupSentinel) {
	_ = os.MkdirAll(sentinelDir, 0o755)
	path := groupSentinelPath(sentinelDir, projectDir, specs)
	data, _ := json.MarshalIndent(gs, "", "  ")
	_ = os.WriteFile(path, append(data, '\n'), 0o644)
}

func removeGroupSentinel(sentinelDir, projectDir string, specs []CommandSpec) {
	path := groupSentinelPath(sentinelDir, projectDir, specs)
	_ = os.Remove(path)
}

func resetGroupOnFailure(cfg *ResolvedConfig, flags SyncCheckFlags, failedSpecKey string) {
	mode := flags.OnFail
	if mode == "" {
		mode = "restart"
	}

	if mode == "retry" {
		group := readGroupSentinel(cfg.SentinelDir, cfg.ProjectDir, flags.Specs)
		var filtered []string
		for _, key := range group.Passed {
			if key != failedSpecKey {
				filtered = append(filtered, key)
			}
		}
		group.Passed = filtered
		writeGroupSentinel(cfg.SentinelDir, cfg.ProjectDir, flags.Specs, group)
	} else {
		removeGroupSentinel(cfg.SentinelDir, cfg.ProjectDir, flags.Specs)
	}
}

// collectedIssue represents an issue found during spec evaluation.
type collectedIssue struct {
	specKey string
	kind    string // "missing", "pending", "timeout", "fail"
	reason  string
}

// RunSyncCheck performs a grouped sequential check.
func RunSyncCheck(cfg *ResolvedConfig, flags SyncCheckFlags, event map[string]interface{}) error {
	// Check global enable
	enabled := false
	for _, spec := range flags.Specs {
		if IsEnabled(spec.Name) {
			enabled = true
			break
		}
	}
	if !enabled {
		return nil
	}

	// Validate all specs exist in config
	for _, spec := range flags.Specs {
		if spec.Type == "exec" {
			if _, ok := cfg.Execs[spec.Name]; !ok {
				return fmt.Errorf("spec %q:%q not found in config", spec.Type, spec.Name)
			}
		} else {
			if _, ok := cfg.Tasks[spec.Name]; !ok {
				return fmt.Errorf("spec %q:%q not found in config", spec.Type, spec.Name)
			}
		}
	}

	// Resolve group limit
	groupLimit := flags.Limit
	if groupLimit == 0 {
		groupLimit = resolveGroupLimit(cfg, flags.Specs)
	}

	// Stop-event recursion guard
	if resp := guardStopEvent(event, groupLimit); resp != nil {
		return emitResponse(*resp)
	}

	// Trigger matching
	patterns := resolveTriggerPatterns(cfg, flags.On, flags.Trigger)
	if len(patterns) > 0 && !matchesTrigger(event, patterns) {
		return nil // allow
	}

	group := readGroupSentinel(cfg.SentinelDir, cfg.ProjectDir, flags.Specs)

	// Precompute change detection for exec specs
	changeCache := precomputeChanges(cfg, flags)
	hashCache := precomputeFingerprints(cfg, flags, changeCache)

	// Session-aware staleness
	marker := ReadMarker(cfg.ProjectDir)
	currentSessionID := ""
	if marker != nil {
		currentSessionID = marker.SessionID
	}

	// Task-level skip-if-no-changes
	taskNoChanges := precomputeTaskNoChanges(cfg)

	var collected []collectedIssue

	for _, spec := range flags.Specs {
		specKey := spec.Type + ":" + spec.Name

		// Skip already-passed specs
		if contains(group.Passed, specKey) {
			continue
		}

		if spec.Type == "exec" {
			execCfg, ok := cfg.Execs[spec.Name]
			if !ok {
				continue
			}

			// Apply group-level always flag
			if flags.Always {
				execCfg.Always = true
			}

			cacheKey := changeCacheKey(execCfg.FileExt, flags.Staged)
			fpKey := fingerprintCacheKey(execCfg.FileExt, flags.Staged)

			var hasChangesPtr *bool
			if v, ok := changeCache[cacheKey]; ok {
				hasChangesPtr = &v
			}
			contentHash := hashCache[fpKey]

			// Trigger matching at group level, skip per-spec
			verdict := preEvaluateExec(cfg, event, execCfg, spec.Name, flags.Staged, "", "", hasChangesPtr, contentHash)

			action := handleExecVerdict(cfg, flags, spec, specKey, group, groupLimit, verdict, &collected)
			if action == "exit" {
				return nil
			}
			continue
		}

		// Task spec
		if taskNoChanges {
			taskCfg, ok := cfg.Tasks[spec.Name]
			if ok && !taskCfg.Always && !flags.Always {
				group.Passed = append(group.Passed, specKey)
				writeGroupSentinel(cfg.SentinelDir, cfg.ProjectDir, flags.Specs, group)
				continue
			}
		}

		sentinel := readTaskResult(cfg.SentinelDir, cfg.ProjectDir, spec.Name, currentSessionID)
		result := evaluateSentinel(sentinel, currentSessionID, "")

		action := handleTaskVerdict(cfg, flags, spec, specKey, group, groupLimit, result, sentinel, &collected)
		if action == "exit" {
			return nil
		}
	}

	// Emit collected issues if any
	if len(collected) > 0 {
		message := buildCollectedMessage(collected)
		hasCounted := false
		var firstCountedKey string
		for _, c := range collected {
			if c.kind == "fail" || c.kind == "timeout" {
				hasCounted = true
				firstCountedKey = c.specKey
				break
			}
		}
		if hasCounted {
			parts := strings.SplitN(firstCountedKey, ":", 2)
			specName := firstCountedKey
			if len(parts) == 2 {
				specName = parts[1]
			}
			return emitResponse(blockWithLimit(cfg, specName, groupLimit, message))
		}
		return emitResponse(blockNoCount(cfg.ProjectDir, message))
	}

	// All specs passed
	removeGroupSentinel(cfg.SentinelDir, cfg.ProjectDir, flags.Specs)
	return nil // allow
}

func handleExecVerdict(cfg *ResolvedConfig, flags SyncCheckFlags, spec CommandSpec, specKey string, group *groupSentinel, groupLimit int, verdict ExecCheckVerdict, collected *[]collectedIssue) string {
	switch verdict.Kind {
	case "skip-trigger", "skip-no-changes":
		group.Passed = append(group.Passed, specKey)
		writeGroupSentinel(cfg.SentinelDir, cfg.ProjectDir, flags.Specs, group)
		return "continue"

	case "pass":
		group.Passed = append(group.Passed, specKey)
		writeGroupSentinel(cfg.SentinelDir, cfg.ProjectDir, flags.Specs, group)
		resetBlockCount(cfg.SentinelDir, cfg.ProjectDir, spec.Name)
		return "continue"

	case "missing":
		reason := fmt.Sprintf("%s has no results. Run it first:\n\n  %s\n\nRetry after the command completes.",
			specLabel(spec), buildSyncRunCommand(spec, flags))
		if flags.Bail {
			_ = emitResponse(blockNoCount(cfg.ProjectDir, reason))
			return "exit"
		}
		*collected = append(*collected, collectedIssue{specKey: specKey, kind: "missing", reason: reason})
		return "continue"

	case "pending":
		timeout := resolveSpecTimeout(cfg, spec)
		sentinel := verdict.Sentinel
		if sentinel != nil && sentinel.StartedAt != "" && timeout > 0 {
			started, err := time.Parse(time.RFC3339, sentinel.StartedAt)
			if err == nil {
				elapsed := time.Since(started).Seconds()
				if elapsed > float64(timeout) {
					removeSentinel(cfg.SentinelDir, cfg.ProjectDir, spec.Name)
					resetGroupOnFailure(cfg, flags, specKey)
					reason := fmt.Sprintf("%s timed out after %ds (timeout: %ds).\n\nInvestigate and re-run: %s",
						specLabel(spec), int(math.Round(elapsed)), timeout, buildSyncRunCommand(spec, flags))
					if flags.Bail {
						_ = emitResponse(blockWithLimit(cfg, spec.Name, groupLimit, reason))
						return "exit"
					}
					// Re-read group after reset
					*group = *readGroupSentinel(cfg.SentinelDir, cfg.ProjectDir, flags.Specs)
					*collected = append(*collected, collectedIssue{specKey: specKey, kind: "timeout", reason: reason})
					return "continue"
				}
			}
		}
		pendingReason := fmt.Sprintf("%s is still running. Wait for completion before retrying.", specLabel(spec))
		if flags.Bail {
			_ = emitResponse(blockNoCount(cfg.ProjectDir, pendingReason))
			return "exit"
		}
		*collected = append(*collected, collectedIssue{specKey: specKey, kind: "pending", reason: pendingReason})
		return "continue"

	case "fail":
		resetGroupOnFailure(cfg, flags, specKey)
		removeSentinel(cfg.SentinelDir, cfg.ProjectDir, spec.Name)
		reason := buildSyncFailMessage(spec, verdict.Sentinel)
		if flags.Bail {
			_ = emitResponse(blockWithLimit(cfg, spec.Name, groupLimit, reason))
			return "exit"
		}
		*group = *readGroupSentinel(cfg.SentinelDir, cfg.ProjectDir, flags.Specs)
		*collected = append(*collected, collectedIssue{specKey: specKey, kind: "fail", reason: reason})
		return "continue"
	}

	return "continue"
}

func handleTaskVerdict(cfg *ResolvedConfig, flags SyncCheckFlags, spec CommandSpec, specKey string, group *groupSentinel, groupLimit int, result CheckResult, sentinel *SentinelData, collected *[]collectedIssue) string {
	switch result.Kind {
	case "pass":
		group.Passed = append(group.Passed, specKey)
		writeGroupSentinel(cfg.SentinelDir, cfg.ProjectDir, flags.Specs, group)
		resetBlockCount(cfg.SentinelDir, cfg.ProjectDir, spec.Name)
		return "continue"

	case "missing":
		reason := buildSyncTaskMissingMessage(cfg, flags, spec)
		if flags.Bail {
			_ = emitResponse(blockNoCount(cfg.ProjectDir, reason))
			return "exit"
		}
		*collected = append(*collected, collectedIssue{specKey: specKey, kind: "missing", reason: reason})
		return "continue"

	case "pending":
		timeout := resolveSpecTimeout(cfg, spec)
		if sentinel != nil && sentinel.StartedAt != "" && timeout > 0 {
			started, err := time.Parse(time.RFC3339, sentinel.StartedAt)
			if err == nil {
				elapsed := time.Since(started).Seconds()
				if elapsed > float64(timeout) {
					removeSentinel(cfg.SentinelDir, cfg.ProjectDir, spec.Name)
					resetGroupOnFailure(cfg, flags, specKey)
					reason := fmt.Sprintf("%s timed out after %ds (timeout: %ds).\n\nInvestigate and re-run: %s",
						specLabel(spec), int(math.Round(elapsed)), timeout, buildSyncRunCommand(spec, flags))
					if flags.Bail {
						_ = emitResponse(blockWithLimit(cfg, spec.Name, groupLimit, reason))
						return "exit"
					}
					*group = *readGroupSentinel(cfg.SentinelDir, cfg.ProjectDir, flags.Specs)
					*collected = append(*collected, collectedIssue{specKey: specKey, kind: "timeout", reason: reason})
					return "continue"
				}
			}
		}
		pendingReason := fmt.Sprintf("%s is still running. Wait for completion before retrying.", specLabel(spec))
		if flags.Bail {
			_ = emitResponse(blockNoCount(cfg.ProjectDir, pendingReason))
			return "exit"
		}
		*collected = append(*collected, collectedIssue{specKey: specKey, kind: "pending", reason: pendingReason})
		return "continue"

	case "fail":
		resetGroupOnFailure(cfg, flags, specKey)
		removeSentinel(cfg.SentinelDir, cfg.ProjectDir, spec.Name)
		reason := buildSyncFailMessage(spec, result.Sentinel)
		if flags.Bail {
			_ = emitResponse(blockWithLimit(cfg, spec.Name, groupLimit, reason))
			return "exit"
		}
		*group = *readGroupSentinel(cfg.SentinelDir, cfg.ProjectDir, flags.Specs)
		*collected = append(*collected, collectedIssue{specKey: specKey, kind: "fail", reason: reason})
		return "continue"
	}

	return "continue"
}

func buildCollectedMessage(issues []collectedIssue) string {
	var sections []string
	sections = append(sections, fmt.Sprintf("## Sync: %d issue(s) need attention\n", len(issues)))

	for _, issue := range issues {
		label := strings.ToUpper(issue.kind)
		sections = append(sections, fmt.Sprintf("### [%s] %s\n", label, issue.specKey))
		sections = append(sections, issue.reason)
		sections = append(sections, "")
	}

	sections = append(sections, "---")
	sections = append(sections, "Address all issues above, then retry. Commands that already passed are preserved.")

	return strings.Join(sections, "\n")
}

func specLabel(spec CommandSpec) string {
	if spec.Type == "exec" {
		return fmt.Sprintf("Exec %q", spec.Name)
	}
	return fmt.Sprintf("Task %q", spec.Name)
}

func buildSyncRunCommand(spec CommandSpec, flags SyncCheckFlags) string {
	if spec.Type == "exec" {
		parts := []string{"chunk hook exec run", spec.Name, "--no-check"}
		if flags.Staged {
			parts = append(parts, "--staged")
		}
		if flags.Always {
			parts = append(parts, "--always")
		}
		return strings.Join(parts, " ")
	}
	return fmt.Sprintf("(spawn subagent for task %q)", spec.Name)
}

func buildSyncFailMessage(spec CommandSpec, sentinel *SentinelData) string {
	if spec.Type == "exec" {
		cmd := spec.Name
		exitCode := 1
		output := "(no output)"
		if sentinel != nil {
			if sentinel.Command != "" {
				cmd = sentinel.Command
			}
			if sentinel.ExitCode != 0 {
				exitCode = sentinel.ExitCode
			}
			if sentinel.Output != "" {
				output = sentinel.Output
			}
		}
		return fmt.Sprintf("%s failed (exit %d).\nCommand: %s\n\nOutput:\n%s\n\nFix the issues and retry.",
			specLabel(spec), exitCode, cmd, output)
	}

	reason := "(no reason provided)"
	if sentinel != nil {
		if sentinel.Details != "" {
			reason = sentinel.Details
		}
	}
	agentDetails := reason
	if sentinel != nil && sentinel.RawResult != "" {
		agentDetails = sentinel.RawResult
	}
	return fmt.Sprintf("Task blocked: issues found. Fix them before stopping.\n\n%s", agentDetails)
}

func buildSyncTaskMissingMessage(cfg *ResolvedConfig, flags SyncCheckFlags, spec CommandSpec) string {
	task, ok := cfg.Tasks[spec.Name]
	if !ok {
		return fmt.Sprintf("Task %q is not configured. Add it to .chunk/hook/config.yml.", spec.Name)
	}

	resultPath := SentinelPath(cfg.SentinelDir, cfg.ProjectDir, spec.Name)
	instructions := loadTaskInstructions(task.Instructions, cfg.ProjectDir)
	schema := resolveTaskSchemaContent(cfg.ProjectDir, task.Schema)

	var parts []string
	parts = append(parts, fmt.Sprintf("Task %q has no result. Spawn a subagent to complete the task.", spec.Name))
	parts = append(parts, "")

	if instructions != "" {
		parts = append(parts, "## Instructions", "", instructions, "")
	}

	parts = append(parts, "## Result format", "",
		"Write the result as a JSON file. Schema:", "",
		"```json", schema, "```", "",
		fmt.Sprintf("Result path: %s", resultPath))

	return strings.Join(parts, "\n")
}

func resolveGroupLimit(cfg *ResolvedConfig, specs []CommandSpec) int {
	for _, spec := range specs {
		if spec.Type == "exec" {
			if exec, ok := cfg.Execs[spec.Name]; ok && exec.Limit > 0 {
				return exec.Limit
			}
		} else {
			if task, ok := cfg.Tasks[spec.Name]; ok && task.Limit > 0 {
				return task.Limit
			}
		}
	}
	return 0
}

func resolveSpecTimeout(cfg *ResolvedConfig, spec CommandSpec) int {
	if spec.Type == "exec" {
		if exec, ok := cfg.Execs[spec.Name]; ok {
			return exec.Timeout
		}
		return 300
	}
	if task, ok := cfg.Tasks[spec.Name]; ok {
		return task.Timeout
	}
	return 600
}

func changeCacheKey(fileExt string, staged bool) string {
	ext := fileExt
	if ext == "" {
		ext = "*"
	}
	mode := "all"
	if staged {
		mode = "staged"
	}
	return ext + "|" + mode
}

func fingerprintCacheKey(fileExt string, staged bool) string {
	ext := fileExt
	if ext == "" {
		ext = "*"
	}
	mode := "all"
	if staged {
		mode = "staged"
	}
	return "fp|" + ext + "|" + mode
}

func precomputeChanges(cfg *ResolvedConfig, flags SyncCheckFlags) map[string]bool {
	cache := map[string]bool{}
	seen := map[string]bool{}

	for _, spec := range flags.Specs {
		if spec.Type != "exec" {
			continue
		}
		execCfg, ok := cfg.Execs[spec.Name]
		if !ok || execCfg.Always || flags.Always {
			continue
		}
		key := changeCacheKey(execCfg.FileExt, flags.Staged)
		if seen[key] {
			continue
		}
		seen[key] = true

		hasChanges, _ := detectChanges(cfg.ProjectDir, execCfg.FileExt, flags.Staged)
		cache[key] = hasChanges
	}

	return cache
}

func precomputeFingerprints(cfg *ResolvedConfig, flags SyncCheckFlags, changeCache map[string]bool) map[string]string {
	cache := map[string]string{}
	seen := map[string]bool{}

	for _, spec := range flags.Specs {
		if spec.Type != "exec" {
			continue
		}
		execCfg, ok := cfg.Execs[spec.Name]
		if !ok {
			continue
		}

		key := fingerprintCacheKey(execCfg.FileExt, flags.Staged)
		if seen[key] {
			continue
		}
		seen[key] = true

		// Only hash specs that have changes
		changeKey := changeCacheKey(execCfg.FileExt, flags.Staged)
		if !execCfg.Always && !flags.Always {
			if hasChanges, ok := changeCache[changeKey]; ok && !hasChanges {
				continue
			}
		}

		hash := computeFingerprint(cfg.ProjectDir, flags.Staged, execCfg.FileExt)
		cache[key] = hash
	}

	return cache
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

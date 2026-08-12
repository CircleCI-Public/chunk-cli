package settings

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"

	udiff "github.com/aymanbagabas/go-udiff"
)

// CommitMatcher is the PreToolUse hook group matcher that chunk manages.
// It targets the Bash tool by name; per Claude Code's hook spec, matcher
// filters only on tool name. Command-content filtering is done via CommitIfFilter
// on individual hook entries.
const CommitMatcher = "Bash"

// CommitIfFilter is the per-entry "if" condition that restricts hook entries
// to git commit commands. The Bash(pattern) syntax is evaluated as a glob
// against the bash command string, not the tool name.
const CommitIfFilter = "Bash(git commit*)"

// legacyCommitMatcher is the old group matcher value written by earlier versions
// of chunk init. Recognised during merge so existing settings can be migrated
// to the current format without leaving a stale duplicate group behind.
const legacyCommitMatcher = "Bash(git commit*)"

// MergeResult holds the computed merge without performing any I/O.
type MergeResult struct {
	Original []byte // existing settings.json content (re-marshaled for normalized formatting)
	Merged   []byte // merged result
	Changed  bool   // false if already up to date
}

// Merge computes the merged settings from existing and generated JSON bytes.
// It preserves all unknown keys in the existing settings and applies chunk's
// generated keys on top. Returns data only — display and file writing are
// the caller's responsibility.
func Merge(existing, generated []byte) (*MergeResult, error) {
	var existingMap map[string]interface{}
	if err := json.Unmarshal(existing, &existingMap); err != nil {
		return nil, fmt.Errorf("parse existing settings: %w", err)
	}

	var generatedMap map[string]interface{}
	if err := json.Unmarshal(generated, &generatedMap); err != nil {
		return nil, fmt.Errorf("parse generated settings: %w", err)
	}

	// Normalize the original for stable comparison.
	originalBytes, err := json.MarshalIndent(existingMap, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal original settings: %w", err)
	}

	// Deep-copy existing via round-trip so mutations don't affect the original.
	var merged map[string]interface{}
	if err := json.Unmarshal(originalBytes, &merged); err != nil {
		return nil, fmt.Errorf("copy existing settings: %w", err)
	}

	// Overwrite $schema and _comment from generated.
	if v, ok := generatedMap["$schema"]; ok {
		merged["$schema"] = v
	}
	if v, ok := generatedMap["_comment"]; ok {
		merged["_comment"] = v
	}

	// Union permissions.allow.
	mergePermissionsAllow(merged, generatedMap)

	// Merge hooks.PreToolUse — replace the chunk-managed hook group by matcher.
	mergeHooks(merged, generatedMap)

	// Merge hooks.Stop — replace the chunk-managed group by command. Without
	// this a repo that already had a settings.json keeps its commit hooks but
	// never gets the Stop hook, so validation stops running at session end.
	mergeStopHooks(merged, generatedMap)

	mergedBytes, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal merged settings: %w", err)
	}

	return &MergeResult{
		Original: originalBytes,
		Merged:   mergedBytes,
		Changed:  !bytes.Equal(originalBytes, mergedBytes),
	}, nil
}

// Diff returns a unified diff string between the original and merged JSON.
// Returns an empty string if there are no differences.
func Diff(original, merged []byte) string {
	return udiff.Unified("current", "proposed", string(original)+"\n", string(merged)+"\n")
}

// mergePermissionsAllow unions the "allow" list under "permissions",
// deduplicating entries and preserving existing ones.
func mergePermissionsAllow(merged, generated map[string]interface{}) {
	genPerms, ok := generated["permissions"].(map[string]interface{})
	if !ok {
		return
	}
	genAllow := toStringSlice(genPerms["allow"])
	if len(genAllow) == 0 {
		return
	}

	// Ensure merged has a permissions map.
	mergedPerms, ok := merged["permissions"].(map[string]interface{})
	if !ok {
		mergedPerms = map[string]interface{}{}
		merged["permissions"] = mergedPerms
	}

	existingAllow := toStringSlice(mergedPerms["allow"])
	seen := make(map[string]bool, len(existingAllow))
	for _, v := range existingAllow {
		seen[v] = true
	}

	for _, v := range genAllow {
		if !seen[v] {
			existingAllow = append(existingAllow, v)
			seen[v] = true
		}
	}

	sort.Strings(existingAllow)

	// Convert back to []interface{} for JSON round-tripping.
	result := make([]interface{}, len(existingAllow))
	for i, v := range existingAllow {
		result[i] = v
	}
	mergedPerms["allow"] = result
}

// mergeHooks replaces the chunk-managed hook group (matched by CommitMatcher)
// within PreToolUse, preserving all other hook types and groups.
func mergeHooks(merged, generated map[string]interface{}) {
	genHooks, ok := generated["hooks"].(map[string]interface{})
	if !ok {
		return
	}
	genPreToolUse, ok := genHooks["PreToolUse"].([]interface{})
	if !ok || len(genPreToolUse) == 0 {
		return
	}

	// Find the chunk-managed group in generated hooks.
	var chunkGroup interface{}
	for _, g := range genPreToolUse {
		group, isMap := g.(map[string]interface{})
		if !isMap {
			continue
		}
		if matcher, _ := group["matcher"].(string); matcher == CommitMatcher {
			chunkGroup = g
			break
		}
	}
	if chunkGroup == nil {
		return
	}

	// Ensure merged has hooks.PreToolUse.
	mergedHooks, ok := merged["hooks"].(map[string]interface{})
	if !ok {
		mergedHooks = map[string]interface{}{}
		merged["hooks"] = mergedHooks
	}

	mergedPreToolUse, ok := mergedHooks["PreToolUse"].([]interface{})
	if !ok {
		mergedPreToolUse = []interface{}{}
	}

	// Replace existing group with same matcher (or legacy matcher), or append.
	replaced := false
	for i, g := range mergedPreToolUse {
		group, isMap := g.(map[string]interface{})
		if !isMap {
			continue
		}
		matcher, _ := group["matcher"].(string)
		if matcher == CommitMatcher || matcher == legacyCommitMatcher {
			mergedPreToolUse[i] = chunkGroup
			replaced = true
			break
		}
	}
	if !replaced {
		mergedPreToolUse = append(mergedPreToolUse, chunkGroup)
	}

	mergedHooks["PreToolUse"] = mergedPreToolUse
}

// mergeStopHooks replaces the chunk-managed Stop hook group (identified by the
// "chunk validate" command) within Stop, preserving all other Stop groups.
func mergeStopHooks(merged, generated map[string]interface{}) {
	genHooks, ok := generated["hooks"].(map[string]interface{})
	if !ok {
		return
	}
	genStop, ok := genHooks["Stop"].([]interface{})
	if !ok || len(genStop) == 0 {
		return
	}

	// Find the chunk-managed group in generated Stop hooks.
	var chunkGroup interface{}
	for _, g := range genStop {
		if isChunkStopGroup(g) {
			chunkGroup = g
			break
		}
	}
	if chunkGroup == nil {
		return
	}

	// Ensure merged has hooks.Stop.
	mergedHooks, ok := merged["hooks"].(map[string]interface{})
	if !ok {
		mergedHooks = map[string]interface{}{}
		merged["hooks"] = mergedHooks
	}

	mergedStop, ok := mergedHooks["Stop"].([]interface{})
	if !ok {
		mergedStop = []interface{}{}
	}

	// Replace existing chunk-managed group, or append.
	replaced := false
	for i, g := range mergedStop {
		if isChunkStopGroup(g) {
			mergedStop[i] = chunkGroup
			replaced = true
			break
		}
	}
	if !replaced {
		mergedStop = append(mergedStop, chunkGroup)
	}

	mergedHooks["Stop"] = mergedStop
}

// isChunkStopGroup reports whether a Stop hook group is chunk-managed,
// identified by containing a hook with command "chunk validate".
func isChunkStopGroup(g interface{}) bool {
	group, ok := g.(map[string]interface{})
	if !ok {
		return false
	}
	hooks, ok := group["hooks"].([]interface{})
	if !ok {
		return false
	}
	for _, h := range hooks {
		entry, ok := h.(map[string]interface{})
		if !ok {
			continue
		}
		if cmd, _ := entry["command"].(string); cmd == "chunk validate" {
			return true
		}
	}
	return false
}

// MergeCodex computes the merged .codex/hooks.json from existing and generated bytes.
// It preserves all unknown keys and hook types, replaces the chunk-managed PreToolUse
// group by matcher, and replaces the chunk-managed Stop hook group by command.
func MergeCodex(existing, generated []byte) (*MergeResult, error) {
	var existingMap map[string]interface{}
	if err := json.Unmarshal(existing, &existingMap); err != nil {
		return nil, fmt.Errorf("parse existing hooks: %w", err)
	}

	var generatedMap map[string]interface{}
	if err := json.Unmarshal(generated, &generatedMap); err != nil {
		return nil, fmt.Errorf("parse generated hooks: %w", err)
	}

	originalBytes, err := json.MarshalIndent(existingMap, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal original hooks: %w", err)
	}

	mergeHooks(existingMap, generatedMap)
	mergeStopHooks(existingMap, generatedMap)

	mergedBytes, err := json.MarshalIndent(existingMap, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal merged hooks: %w", err)
	}

	return &MergeResult{
		Original: originalBytes,
		Merged:   mergedBytes,
		Changed:  !bytes.Equal(originalBytes, mergedBytes),
	}, nil
}

// toStringSlice converts an interface{} (expected []interface{} of strings)
// to a []string. Returns nil for non-matching types.
func toStringSlice(v interface{}) []string {
	arr, ok := v.([]interface{})
	if !ok {
		return nil
	}
	result := make([]string, 0, len(arr))
	for _, item := range arr {
		if s, ok := item.(string); ok {
			result = append(result, s)
		}
	}
	return result
}

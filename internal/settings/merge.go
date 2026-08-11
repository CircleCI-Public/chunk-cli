package settings

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	udiff "github.com/aymanbagabas/go-udiff"
)

// CommitMatcher is the PreToolUse hook group matcher that chunk manages.
// Targets the Bash tool by name; per Claude Code's hook spec, matcher
// filters only on tool name.
const CommitMatcher = "Bash"

// CommitIfFilter is the per-entry "if" condition that restricts hook entries
// to git commit commands.
const CommitIfFilter = "Bash(git commit*)"

// legacyCommitMatcher is the old group matcher written by earlier versions of
// chunk init. Recognised during merge so existing settings can be migrated
// without leaving a stale duplicate group behind.
const legacyCommitMatcher = "Bash(git commit*)"

// FixMatcher is the PostToolUse hook matcher chunk manages for running fix commands.
const FixMatcher = "Edit|Write"

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

// mergeHooks replaces chunk-managed hook groups within PreToolUse and
// PostToolUse, preserving all other hook types and groups.
func mergeHooks(merged, generated map[string]interface{}) {
	genHooks, ok := generated["hooks"].(map[string]interface{})
	if !ok {
		return
	}

	mergedHooks, ok := merged["hooks"].(map[string]interface{})
	if !ok {
		mergedHooks = map[string]interface{}{}
		merged["hooks"] = mergedHooks
	}

	mergeHookType(mergedHooks, genHooks, "PreToolUse", CommitMatcher)
	mergeHookType(mergedHooks, genHooks, "PreToolUse", legacyCommitMatcher)
	mergeHookType(mergedHooks, genHooks, "PostToolUse", FixMatcher)
	mergeSessionStartHooks(mergedHooks, genHooks)
}

// mergeSessionStartHooks replaces the chunk-managed SessionStart hook group
// (identified by the "chunk session start" command) within SessionStart,
// preserving all other SessionStart groups.
func mergeSessionStartHooks(mergedHooks, genHooks map[string]interface{}) {
	genGroups, _ := genHooks["SessionStart"].([]interface{})

	var chunkGroup interface{}
	for _, g := range genGroups {
		if isChunkSessionStartGroup(g) {
			chunkGroup = g
			break
		}
	}

	mergedGroups, _ := mergedHooks["SessionStart"].([]interface{})

	var filtered []interface{}
	for _, g := range mergedGroups {
		if !isChunkSessionStartGroup(g) {
			filtered = append(filtered, g)
		}
	}
	if chunkGroup != nil {
		filtered = append(filtered, chunkGroup)
	}

	if len(filtered) > 0 {
		mergedHooks["SessionStart"] = filtered
	} else {
		delete(mergedHooks, "SessionStart")
	}
}

// isChunkSessionStartGroup reports whether a hook group is chunk's managed
// SessionStart group, identified by containing a "chunk session start" command.
func isChunkSessionStartGroup(g interface{}) bool {
	group, ok := g.(map[string]interface{})
	if !ok {
		return false
	}
	hooks, _ := group["hooks"].([]interface{})
	for _, h := range hooks {
		hook, ok := h.(map[string]interface{})
		if !ok {
			continue
		}
		if cmd, _ := hook["command"].(string); strings.Contains(cmd, "chunk session start") {
			return true
		}
	}
	return false
}

// mergeHookType replaces the chunk-managed group (identified by matcher) within
// hookType, preserving all other groups. When the generated config has no group
// for the matcher, any existing chunk-managed group in the merged config is removed.
func mergeHookType(mergedHooks, genHooks map[string]interface{}, hookType, matcher string) {
	genGroups, _ := genHooks[hookType].([]interface{})

	// Find the chunk-managed group in generated hooks for this type.
	var chunkGroup interface{}
	for _, g := range genGroups {
		group, isMap := g.(map[string]interface{})
		if !isMap {
			continue
		}
		if m, _ := group["matcher"].(string); m == matcher {
			chunkGroup = g
			break
		}
	}

	mergedGroups, _ := mergedHooks[hookType].([]interface{})

	// Remove any existing chunk-managed group from merged.
	filtered := mergedGroups[:0]
	for _, g := range mergedGroups {
		group, isMap := g.(map[string]interface{})
		if isMap {
			if m, _ := group["matcher"].(string); m == matcher {
				continue
			}
		}
		filtered = append(filtered, g)
	}

	if chunkGroup != nil {
		filtered = append(filtered, chunkGroup)
	}

	if len(filtered) > 0 {
		mergedHooks[hookType] = filtered
	} else {
		delete(mergedHooks, hookType)
	}
}

// MergeCodex computes the merged .codex/hooks.json from existing and generated bytes.
// It preserves all unknown keys and hook types, and replaces chunk-managed hook groups by matcher.
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

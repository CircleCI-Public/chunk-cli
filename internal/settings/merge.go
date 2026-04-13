package settings

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
)

// CommitMatcher is the hook matcher string that chunk manages.
const CommitMatcher = "Bash(git commit*)"

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
		group, ok := g.(map[string]interface{})
		if !ok {
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

	// Replace existing group with same matcher, or append.
	replaced := false
	for i, g := range mergedPreToolUse {
		group, ok := g.(map[string]interface{})
		if !ok {
			continue
		}
		if matcher, _ := group["matcher"].(string); matcher == CommitMatcher {
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

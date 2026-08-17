package settings

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
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

// StopCommand is the Stop hook command that chunk manages. Merge identifies
// chunk's own Stop entry by this exact string, so it must stay in sync with the
// command written by Build and BuildCodex.
const StopCommand = "chunk validate"

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

	// Merge hooks.PreToolUse and hooks.Stop — replace what chunk owns, keep the
	// rest. Without the Stop half a repo that already had a settings.json keeps
	// its commit hooks but never gets the Stop hook, so validation stops running
	// at session end.
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

// mergeHooks installs chunk's hooks into merged, preserving every hook type,
// group, and entry chunk does not own.
//
// The two hook types chunk writes are owned at different granularities, so they
// merge differently:
//
//   - PreToolUse: chunk owns a whole group, identified by CommitMatcher, so the
//     group is replaced outright. The legacy matcher counts as a match too, so
//     settings written by older versions migrate in place.
//   - Stop: chunk owns a single entry, identified by StopCommand. A user may
//     have added their own entries to the group holding it, so the entry is
//     replaced where it sits and its siblings are left alone — replacing the
//     enclosing group would silently delete them. Only when no chunk entry
//     exists anywhere is chunk's own group appended.
func mergeHooks(merged, generated map[string]interface{}) {
	genHooks, ok := generated["hooks"].(map[string]interface{})
	if !ok {
		return
	}

	// PreToolUse: swap chunk's group for the one already there.
	genPreToolUse, _ := genHooks["PreToolUse"].([]interface{})
	if i := slices.IndexFunc(genPreToolUse, isChunkCommitGroup); i >= 0 {
		mergedHooks := hooksMap(merged)
		mergedPreToolUse, _ := mergedHooks["PreToolUse"].([]interface{})
		mergedHooks["PreToolUse"] = replaceOrAppend(mergedPreToolUse, isChunkCommitGroup, genPreToolUse[i])
	}

	// Stop: find chunk's entry in the generated hooks, and the group holding it.
	genStop, _ := genHooks["Stop"].([]interface{})
	var genGroup, genEntry interface{}
	for _, g := range genStop {
		_, entries, isGroup := stopGroupEntries(g)
		if !isGroup {
			continue
		}
		if i := slices.IndexFunc(entries, isChunkStopEntry); i >= 0 {
			genGroup, genEntry = g, entries[i]
			break
		}
	}
	if genEntry == nil {
		return
	}

	// Update chunk's entry wherever it already lives, keeping the user's own
	// entries in that group intact.
	mergedHooks := hooksMap(merged)
	mergedStop, _ := mergedHooks["Stop"].([]interface{})
	for _, g := range mergedStop {
		group, entries, isGroup := stopGroupEntries(g)
		if !isGroup || !slices.ContainsFunc(entries, isChunkStopEntry) {
			continue
		}
		group["hooks"] = replaceOrAppend(entries, isChunkStopEntry, genEntry)
		mergedHooks["Stop"] = mergedStop
		return
	}

	mergedHooks["Stop"] = append(mergedStop, genGroup)
}

// hooksMap returns the "hooks" object in settings, creating it when absent.
// Created lazily: adding an empty hooks object to settings that have none would
// count as a change and prompt the user over nothing.
func hooksMap(settings map[string]interface{}) map[string]interface{} {
	hooks, ok := settings["hooks"].(map[string]interface{})
	if !ok {
		hooks = map[string]interface{}{}
		settings["hooks"] = hooks
	}
	return hooks
}

// isChunkCommitGroup reports whether a PreToolUse group is the chunk-managed
// commit group, accepting the legacy matcher so older settings migrate in place.
func isChunkCommitGroup(g interface{}) bool {
	group, ok := g.(map[string]interface{})
	if !ok {
		return false
	}
	matcher, _ := group["matcher"].(string)
	return matcher == CommitMatcher || matcher == legacyCommitMatcher
}

// stopGroupEntries returns a Stop hook group's map and its list of hook entries.
func stopGroupEntries(g interface{}) (map[string]interface{}, []interface{}, bool) {
	group, ok := g.(map[string]interface{})
	if !ok {
		return nil, nil, false
	}
	entries, ok := group["hooks"].([]interface{})
	if !ok {
		return nil, nil, false
	}
	return group, entries, true
}

// isChunkStopEntry reports whether a Stop hook entry is the one chunk manages,
// identified by its command.
func isChunkStopEntry(h interface{}) bool {
	entry, ok := h.(map[string]interface{})
	if !ok {
		return false
	}
	cmd, _ := entry["command"].(string)
	return cmd == StopCommand
}

// replaceOrAppend replaces the first element of s that match reports true for
// with v, appending v when nothing matches. Returns the possibly-grown slice.
func replaceOrAppend[T any](s []T, match func(T) bool, v T) []T {
	if i := slices.IndexFunc(s, match); i >= 0 {
		s[i] = v
		return s
	}
	return append(s, v)
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

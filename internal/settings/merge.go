package settings

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"sort"

	udiff "github.com/aymanbagabas/go-udiff"
)

// CommitMatcher is the PreToolUse hook group matcher that chunk manages for
// commit checks. It targets the Bash tool by name; per Claude Code's hook spec,
// matcher filters only on tool name. Command-content filtering is done via
// CommitIfFilter on individual hook entries.
const CommitMatcher = "Bash"

// CommitIfFilter is the per-entry "if" condition that restricts hook entries
// to git commit commands. The Bash(pattern) syntax is evaluated as a glob
// against the bash command string, not the tool name.
const CommitIfFilter = "Bash(git commit*)"

// WriteFileMatcher is the PreToolUse hook group matcher for the Write tool.
// The write-guard hook blocks AI-tool config files that don't belong in this project.
const WriteFileMatcher = "Write"

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

	// Merge hooks.PreToolUse and hooks.Stop — replace the entries chunk owns, keep
	// the rest. Without the Stop half a repo that already had a settings.json keeps
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
// Both hook types chunk writes are owned per entry, not per group. A user may
// have added their own entries to a group that also holds chunk's, and replacing
// the enclosing group would silently delete them. What chunk owns:
//
//   - PreToolUse: entries carrying CommitIfFilter, plus every entry of a group
//     still on the legacy matcher — older versions wrote that group whole and
//     its entries have no "if" to recognise them by.
//   - PreToolUse: every entry in the Write matcher group (chunk owns all of them).
//   - Stop: entries whose command is StopCommand.
func mergeHooks(merged, generated map[string]interface{}) {
	genHooks, ok := generated["hooks"].(map[string]interface{})
	if !ok {
		return
	}
	mergeHookType(merged, genHooks, "PreToolUse", ownsCommitEntry, isChunkCommitGroup)
	mergeHookType(merged, genHooks, "PreToolUse", ownsWriteEntry, isChunkWriteGroup)
	mergeHookType(merged, genHooks, "Stop", ownsStopEntry, nil)
}

// entryOwner reports whether chunk owns an entry, given the group holding it.
type entryOwner func(group map[string]interface{}, entry interface{}) bool

// mergeHookType installs chunk's entries for one hook type. Chunk's entries are
// stripped from wherever they sit — collapsing stale duplicates left behind by
// older versions — and the generated ones go back in at the first position they
// held, so a merge over already-merged settings is a no-op.
//
// With nothing of chunk's present, isTargetGroup picks an existing group to write
// into. PreToolUse needs it: chunk's group is identified by tool name, so
// appending a second group on the same matcher would be wrong. Stop groups have
// no matcher, so it passes nil and chunk's own group is appended.
func mergeHookType(merged, genHooks map[string]interface{}, hookType string, owns entryOwner, isTargetGroup func(map[string]interface{}) bool) {
	genGroup, genEntries := chunkEntries(genHooks[hookType], owns)
	if len(genEntries) == 0 {
		return
	}

	mergedHooks := hooksMap(merged)
	groups, _ := mergedHooks[hookType].([]interface{})

	// Strip chunk's entries out of every group, noting where the first one sat and
	// which groups held nothing else.
	targetIdx, insertAt := -1, 0
	emptied := make(map[int]bool)
	for i, g := range groups {
		group, entries, isGroup := groupEntries(g)
		if !isGroup {
			continue
		}
		kept := make([]interface{}, 0, len(entries))
		for _, e := range entries {
			if owns(group, e) {
				if targetIdx < 0 {
					targetIdx, insertAt = i, len(kept)
				}
				continue
			}
			kept = append(kept, e)
		}
		if len(kept) == len(entries) {
			continue
		}
		group["hooks"] = kept
		if len(kept) == 0 {
			emptied[i] = true
		}
	}

	if targetIdx < 0 && isTargetGroup != nil {
		for i, g := range groups {
			group, entries, isGroup := groupEntries(g)
			if isGroup && isTargetGroup(group) {
				targetIdx, insertAt = i, len(entries)
				break
			}
		}
	}
	if targetIdx < 0 {
		mergedHooks[hookType] = append(groups, chunkGroup(genGroup, genEntries))
		return
	}

	target, entries, _ := groupEntries(groups[targetIdx])
	target["hooks"] = slices.Insert(entries, insertAt, genEntries...)
	// Carry over the generated group's own keys — its matcher above all — so a
	// group still on the legacy matcher migrates in place.
	for k, v := range genGroup {
		if k != "hooks" {
			target[k] = v
		}
	}
	delete(emptied, targetIdx)

	kept := make([]interface{}, 0, len(groups))
	for i, g := range groups {
		if !emptied[i] {
			kept = append(kept, g)
		}
	}
	mergedHooks[hookType] = kept
}

// chunkEntries returns the generated group holding chunk's entries for one hook
// type, along with those entries.
func chunkEntries(genGroups interface{}, owns entryOwner) (map[string]interface{}, []interface{}) {
	list, _ := genGroups.([]interface{})
	for _, g := range list {
		group, entries, isGroup := groupEntries(g)
		if !isGroup {
			continue
		}
		owned := make([]interface{}, 0, len(entries))
		for _, e := range entries {
			if owns(group, e) {
				owned = append(owned, e)
			}
		}
		if len(owned) > 0 {
			return group, owned
		}
	}
	return nil, nil
}

// chunkGroup builds a fresh hook group from the generated group's own fields and
// the given entries, so the generated map is never aliased into merged settings.
func chunkGroup(gen map[string]interface{}, entries []interface{}) map[string]interface{} {
	group := make(map[string]interface{}, len(gen))
	for k, v := range gen {
		if k != "hooks" {
			group[k] = v
		}
	}
	group["hooks"] = entries
	return group
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

// isChunkCommitGroup reports whether a PreToolUse group is the one chunk writes
// its commit hooks into, accepting the legacy matcher so older settings migrate
// in place rather than gaining a second group on the same tool.
func isChunkCommitGroup(group map[string]interface{}) bool {
	matcher, _ := group["matcher"].(string)
	return matcher == CommitMatcher || matcher == legacyCommitMatcher
}

// isChunkWriteGroup reports whether a PreToolUse group is the one chunk writes
// its Write guard hook into.
func isChunkWriteGroup(group map[string]interface{}) bool {
	matcher, _ := group["matcher"].(string)
	return matcher == WriteFileMatcher
}

// ownsWriteEntry reports whether a PreToolUse entry belongs to chunk's Write
// guard group. Chunk owns all entries in the Write matcher group.
func ownsWriteEntry(group map[string]interface{}, _ interface{}) bool {
	matcher, _ := group["matcher"].(string)
	return matcher == WriteFileMatcher
}

// groupEntries returns a hook group's map and its list of hook entries.
func groupEntries(g interface{}) (map[string]interface{}, []interface{}, bool) {
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

// ownsCommitEntry reports whether a PreToolUse entry is one of chunk's commit
// hooks. Entries are tagged with CommitIfFilter; those in a group still on the
// legacy matcher are not, but that whole group was written by chunk.
func ownsCommitEntry(group map[string]interface{}, e interface{}) bool {
	if matcher, _ := group["matcher"].(string); matcher == legacyCommitMatcher {
		return true
	}
	entry, ok := e.(map[string]interface{})
	if !ok {
		return false
	}
	cond, _ := entry["if"].(string)
	return cond == CommitIfFilter
}

// ownsStopEntry reports whether a Stop entry is the one chunk manages,
// identified by its command.
func ownsStopEntry(_ map[string]interface{}, e interface{}) bool {
	entry, ok := e.(map[string]interface{})
	if !ok {
		return false
	}
	cmd, _ := entry["command"].(string)
	return cmd == StopCommand
}

// MergeCodex computes the merged .codex/hooks.json from existing and generated bytes.
// It preserves all unknown keys and hook types, and replaces chunk's own PreToolUse
// and Stop hook entries via the same mergeHooks used for .claude/settings.json.
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

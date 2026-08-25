package sidecar

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
)

// SnapshotCriteria describes the repository a sidecar is being created for.
// Both fields are optional: an empty Repo or Stack simply contributes no score.
type SnapshotCriteria struct {
	// Repo is the repository name, e.g. "chunk-cli".
	Repo string
	// Stack is the detected tech stack as envbuilder names it, e.g. "go" or
	// "typescript". Callers pass "" when detection failed.
	Stack string
}

// SnapshotMatch is a selected snapshot together with why it was selected.
type SnapshotMatch struct {
	Snapshot circleci.Snapshot
	// Reason is a short human-readable justification, e.g. "matches repo
	// chunk-cli". Callers surface it so the choice is never silent.
	Reason string
}

// Scores are spread an order of magnitude apart so a weaker signal can never
// outrank a stronger one, however many weak signals a name accumulates.
const (
	scoreRepoExact = 1000 // whole name is the repo name
	scoreRepoToken = 100  // repo name appears as a token, e.g. "chunk-cli-go"
	scoreStack     = 10   // stack (or a known alias) appears as a token
	scoreOwned     = 1    // tiebreak: the org's own snapshot over a system one
)

// stackAliases maps an envbuilder stack name to the tokens a snapshot name
// plausibly uses for it. The stack name itself is always matched, so entries
// here list only the extra spellings — including the cimg image names, since
// snapshots are frequently named after the image they were built from.
var stackAliases = map[string][]string{
	"go":         {"golang"},
	"javascript": {"js", "node", "nodejs"},
	"typescript": {"ts", "node", "nodejs"},
	"python":     {"py"},
	"ruby":       {"rb"},
	"java":       {"jvm", "openjdk", "jdk"},
	"scala":      {"jvm", "openjdk", "jdk", "sbt"},
	"dotnet":     {"net", "csharp", "dot-net"},
	"cpp":        {"c++", "cplusplus"},
	"rust":       {"rs", "cargo"},
	"elixir":     {"ex", "beam"},
	"haskell":    {"hs", "ghc"},
	"dart":       {"flutter"},
}

// nameTokens splits a snapshot name and tag into lowercase alphanumeric tokens.
// Matching is token-based rather than substring-based on purpose: a substring
// test makes "go" match "mongo-api" and "django", which is exactly the kind of
// accidental hit that picks the wrong environment.
func nameTokens(s circleci.Snapshot) map[string]bool {
	tokens := map[string]bool{}
	for _, field := range []string{s.Name, s.Tag} {
		for _, tok := range strings.FieldsFunc(strings.ToLower(field), isSeparator) {
			tokens[tok] = true
		}
	}
	return tokens
}

// isSeparator reports whether r ends a token. '+' is kept so "c++" survives as
// one token rather than splitting into an empty string.
func isSeparator(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '+':
		return false
	default:
		return true
	}
}

// normalizeName lowercases a snapshot name and collapses separators so that
// "Chunk CLI" and "chunk-cli" compare equal. It defers to isSeparator for what
// counts as a separator: the two must agree, or a name normalizes into parts
// that nameTokens can never produce and the token match silently fails.
func normalizeName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if isSeparator(r) {
			b.WriteRune('-')
			continue
		}
		b.WriteRune(r)
	}
	return strings.Trim(b.String(), "-")
}

// scoreSnapshot rates how well a snapshot fits the criteria, returning the
// score and the reason for the strongest signal that contributed to it. A
// score of zero means nothing about the snapshot ties it to this repo.
func scoreSnapshot(s circleci.Snapshot, c SnapshotCriteria) (int, string) {
	tokens := nameTokens(s)
	score, reason := 0, ""

	if repo := normalizeName(c.Repo); repo != "" {
		switch {
		case normalizeName(s.Name) == repo:
			score += scoreRepoExact
			reason = fmt.Sprintf("named for repo %s", c.Repo)
		case containsAll(tokens, strings.Split(repo, "-")):
			score += scoreRepoToken
			reason = fmt.Sprintf("mentions repo %s", c.Repo)
		}
	}

	if stack := strings.ToLower(c.Stack); stack != "" {
		for _, alias := range append([]string{stack}, stackAliases[stack]...) {
			if tokens[alias] {
				score += scoreStack
				if reason == "" {
					reason = fmt.Sprintf("built for %s", c.Stack)
				}
				break
			}
		}
	}

	// Only a tiebreak, and only among snapshots that already matched:
	// preferring an owned snapshot must never promote an unrelated one.
	if score > 0 && !s.IsSystem {
		score += scoreOwned
	}
	return score, reason
}

// containsAll reports whether every part is present in tokens. Empty parts are
// ignored so a doubled separator in a repo name does not fail the match, which
// leaves an all-empty parts list vacuously true — the sole caller only reaches
// here with a normalized name that has at least one real token.
func containsAll(tokens map[string]bool, parts []string) bool {
	for _, p := range parts {
		if p != "" && !tokens[p] {
			return false
		}
	}
	return true
}

// SelectSnapshot picks the snapshot that best fits criteria, reporting false
// when none of them relate to the repository at all. Returning false rather
// than an arbitrary snapshot is deliberate: booting the wrong prepared
// environment is more confusing than booting the plain default image, because
// the failures it produces look like the repo's own.
func SelectSnapshot(snapshots []circleci.Snapshot, c SnapshotCriteria) (SnapshotMatch, bool) {
	// Sort by ID first so the winner is stable when two snapshots tie and the
	// API returns them in a different order between runs.
	ranked := make([]circleci.Snapshot, len(snapshots))
	copy(ranked, snapshots)
	sort.Slice(ranked, func(i, j int) bool { return ranked[i].ID < ranked[j].ID })

	best, bestScore, bestReason := circleci.Snapshot{}, 0, ""
	for _, s := range ranked {
		score, reason := scoreSnapshot(s, c)
		if score > bestScore {
			best, bestScore, bestReason = s, score, reason
		}
	}
	if bestScore == 0 {
		return SnapshotMatch{}, false
	}
	return SnapshotMatch{Snapshot: best, Reason: bestReason}, true
}

// ResolveSnapshot lists the org's snapshots and selects the one that best fits
// criteria. It reports false when the org has no suitable snapshot.
func ResolveSnapshot(ctx context.Context, client *circleci.Client, orgID string, c SnapshotCriteria) (SnapshotMatch, bool, error) {
	snapshots, err := client.ListSnapshots(ctx, orgID)
	if err != nil {
		return SnapshotMatch{}, false, fmt.Errorf("list snapshots: %w", err)
	}
	match, ok := SelectSnapshot(snapshots, c)
	return match, ok, nil
}

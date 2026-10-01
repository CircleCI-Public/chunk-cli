package watchd

import (
	"regexp"

	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
)

// treeSHARe matches the object name `git write-tree` prints.
var treeSHARe = regexp.MustCompile(`^[0-9a-f]{40,64}$`)

// treeScript snapshots the sandbox's working tree into a tree object and prints
// its name, using a throwaway index (seeded from the real one for speed) so
// nothing in the sandbox is staged. It is the sandbox's counterpart of treeNow.
const treeScriptBody = `IDX=$(mktemp) || exit 1
cp "$(git rev-parse --git-path index)" "$IDX" 2>/dev/null
GIT_INDEX_FILE="$IDX" git add -A >/dev/null 2>&1 || { rm -f "$IDX"; exit 1; }
GIT_INDEX_FILE="$IDX" git write-tree
rc=$?
rm -f "$IDX"
exit $rc`

// baselineScript prints the tree of the sandbox as the reviewers saw it, which is
// the user's files at the time of the last sync. The fix is whatever differs from
// it afterwards, so the user's own uncommitted work is never part of the diff.
func baselineScript(repoPath string) string {
	return "cd " + sidecar.ShellEscape(repoPath) + " || exit 1\n" + treeScriptBody
}

// diffScript prints the patch between baseline and the sandbox's tree now, then
// reverses it so the sandbox is back as it was for the next user of the pool.
func diffScript(repoPath, baseline string) string {
	return "cd " + sidecar.ShellEscape(repoPath) + " || exit 1\n" +
		"BASE=" + sidecar.ShellEscape(baseline) + "\n" +
		"NOW=$(\n" + treeScriptBody + "\n) || exit 1\n" +
		`P=$(mktemp) || exit 1
git diff --binary --no-renames "$BASE" "$NOW" > "$P"
rc=$?
cat "$P"
git apply -R "$P" >/dev/null 2>&1
rm -f "$P"
exit $rc`
}

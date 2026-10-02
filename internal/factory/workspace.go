package factory

import (
	"context"
	"fmt"
	"strings"

	"github.com/CircleCI-Public/chunk-cli/internal/review"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
)

// workspace is the implementer's checkout on its sidecar. The loop keeps the
// implementer's work as uncommitted changes on top of a baseline commit, so
// `git diff HEAD` there, and on every reviewer it is relayed to, is exactly
// the work under review.
type workspace struct {
	exec     review.Execer
	entry    *sidecar.PoolEntry
	baseline string
}

// Change is the implementer's work so far, relative to the baseline.
type Change struct {
	// Fingerprint identifies the content of the change, so a round in which
	// the implementer changed nothing can be detected.
	Fingerprint string
	// Stat is git's one-line summary, such as "3 files changed, 40 insertions(+)".
	Stat string
}

// Empty reports whether the implementer has changed nothing.
func (c Change) Empty() bool {
	return c.Stat == ""
}

// gitIdentity commits as chunk whatever the sidecar image's git config says:
// an image may have no identity set, require signed commits, or install hooks.
const gitIdentity = "git -c user.name=chunk -c user.email=chunk@circleci.com -c commit.gpgsign=false"

// commitBaseline commits whatever the developer's synced tree holds, so their
// own uncommitted work is part of the baseline rather than of the change the
// reviewers see. The commit is made even when the tree is clean, so the
// baseline is always a commit chunk made.
func (w *workspace) commitBaseline(ctx context.Context) error {
	script := fmt.Sprintf(`cd %s && git add -A && %s commit -q --no-verify --allow-empty -m "chunk factory baseline" && git rev-parse HEAD`,
		sidecar.ShellEscape(w.entry.RepoPath), gitIdentity)
	out, err := w.run(ctx, script)
	if err != nil {
		return fmt.Errorf("commit baseline: %w", err)
	}
	w.baseline = strings.TrimSpace(out)
	return nil
}

// collect gathers the implementer's work as uncommitted changes against the
// baseline and describes it. Commits the implementer made despite being told
// not to are folded back into the working tree, and new files are marked
// intent-to-add so `git diff HEAD` shows them alongside edits.
func (w *workspace) collect(ctx context.Context) (Change, error) {
	if w.baseline == "" {
		return Change{}, fmt.Errorf("collect changes: no baseline commit")
	}
	script := fmt.Sprintf(`cd %s || exit 1
if [ "$(git rev-parse HEAD)" != %s ]; then git reset -q --soft %s || exit 1; fi
git add -A -N || exit 1
git diff HEAD --binary | sha256sum | cut -d' ' -f1
git diff HEAD --shortstat`,
		sidecar.ShellEscape(w.entry.RepoPath), sidecar.ShellEscape(w.baseline), sidecar.ShellEscape(w.baseline))
	out, err := w.run(ctx, script)
	if err != nil {
		return Change{}, fmt.Errorf("collect changes: %w", err)
	}
	lines := strings.SplitN(strings.TrimSpace(out), "\n", 2)
	c := Change{Fingerprint: strings.TrimSpace(lines[0])}
	if len(lines) == 2 {
		c.Stat = strings.TrimSpace(lines[1])
	}
	return c, nil
}

// maxScriptOutput caps what a workspace script may print. The largest is the
// patch of the implementer's whole change; past this something has gone wrong.
const maxScriptOutput = 32 << 20

// run executes script on the implementer's sidecar and returns its stdout.
func (w *workspace) run(ctx context.Context, script string) (string, error) {
	return review.RunScript(ctx, w.exec, w.entry, script, maxScriptOutput)
}

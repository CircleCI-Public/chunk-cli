package eval

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/CircleCI-Public/chunk-cli/internal/gitexec"
)

// Limits on the commits Commits makes into cases.
const (
	// maxCommitPatch skips commits too large to review in one sitting, which
	// would cost the most and say the least about any one prompt.
	maxCommitPatch = 48 << 10
	// commitScanFactor bounds how far back Commits looks for n usable
	// commits, so a history of skipped ones is not walked to its root.
	commitScanFactor = 10
)

// ErrNoCases is returned by Commits when no commit it looked at makes a case.
var ErrNoCases = errors.New("no commit makes a case")

// Commits makes cases of the n most recent usable commits on the first-parent
// history of rev: each is its diff against its parent, with its message as the
// request. The cases are unlabeled; the grader scores them.
//
// Merges, bots' commits, commits that change only documentation or nothing at
// all, and patches over maxCommitPatch are skipped, since a review of them
// says little about the prompts. Fewer than n cases means the history ran out.
func Commits(ctx context.Context, root, rev string, n int) (Suite, error) {
	if n < 1 {
		return Suite{}, fmt.Errorf("need at least one commit, not %d", n)
	}
	// rev-list takes no -- before a revision, so one starting with - would
	// read as an option.
	if strings.HasPrefix(rev, "-") {
		return Suite{}, fmt.Errorf("revision %q must not start with -", rev)
	}
	git := gitexec.Runner{Dir: root}
	out, err := git.Output(ctx, "rev-list", "--first-parent", "--no-merges",
		fmt.Sprintf("--max-count=%d", n*commitScanFactor), rev, "--")
	if err != nil {
		return Suite{}, fmt.Errorf("list the commits of %s: %w", rev, err)
	}
	var cases []Case
	for _, sha := range strings.Fields(string(out)) {
		if len(cases) == n {
			break
		}
		c, ok, err := commitCase(ctx, git, sha)
		if err != nil {
			return Suite{}, err
		}
		if ok {
			cases = append(cases, c)
		}
	}
	if len(cases) == 0 {
		return Suite{}, fmt.Errorf("%w on %s", ErrNoCases, rev)
	}
	return Suite{Cases: cases}, nil
}

// commitCase makes a case of one commit, or reports that it is not one.
func commitCase(ctx context.Context, git gitexec.Runner, sha string) (Case, bool, error) {
	info, err := git.Output(ctx, "show", "-s", "--format=%P%x00%an%x00%s%x00%B", sha)
	if err != nil {
		return Case{}, false, fmt.Errorf("read commit %s: %w", sha, err)
	}
	fields := strings.SplitN(string(info), "\x00", 4)
	if len(fields) < 4 {
		return Case{}, false, fmt.Errorf("read commit %s: unexpected output", sha)
	}
	parents, author, subject, message := strings.Fields(fields[0]), fields[1], fields[2], strings.TrimSpace(fields[3])
	// A root commit has no parent to apply to.
	if len(parents) != 1 || isBot(author) || message == "" {
		return Case{}, false, nil
	}
	// -z, so a path with a space in it stays one path and none is quoted.
	names, err := git.Output(ctx, "diff", "--name-only", "-z", parents[0], sha)
	if err != nil {
		return Case{}, false, fmt.Errorf("list the files commit %s changes: %w", sha, err)
	}
	if !changesCode(splitNUL(string(names))) {
		return Case{}, false, nil
	}
	patch, err := git.Output(ctx, "diff", "--binary", parents[0], sha)
	if err != nil {
		return Case{}, false, fmt.Errorf("diff commit %s: %w", sha, err)
	}
	if len(patch) > maxCommitPatch {
		return Case{}, false, nil
	}
	short := sha[:min(len(sha), 12)]
	return Case{
		Name:     "commit-" + short,
		Revision: parents[0],
		Request:  message,
		Patch:    short + ".patch",
		Source:   "commit " + short + ": " + subject,
		patch:    patch,
	}, true, nil
}

// isBot reports whether a commit's author name is a bot's, such as
// renovate[bot]: its dependency bumps are not what review prompts are for.
func isBot(author string) bool {
	return strings.HasSuffix(strings.ToLower(strings.TrimSpace(author)), "[bot]")
}

// splitNUL is s split on NUL, without the empty field a trailing NUL leaves.
func splitNUL(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == 0 })
}

// changesCode reports whether any of files is more than documentation.
func changesCode(files []string) bool {
	for _, f := range files {
		switch strings.ToLower(path.Ext(f)) {
		case ".md", ".mdx", ".txt", ".rst":
			continue
		}
		return true
	}
	return false
}

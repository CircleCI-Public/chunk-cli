// Package planrepo finds the local checkout a factory plan is planned and
// built in, from the GitHub repositories the plan records.
package planrepo

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/gitremote"
)

// Matches reports whether root's origin is a GitHub checkout of one of
// repos, each OWNER/NAME, compared case-insensitively. own is root's
// OWNER/NAME, "" when its origin is missing or not on GitHub.
func Matches(ctx context.Context, root string, repos []string) (ok bool, own string) {
	url, err := gitremote.URL(ctx, root, "origin")
	if err != nil {
		return false, ""
	}
	org, repo, err := gitremote.ParseRemoteURL(url)
	if err != nil {
		return false, ""
	}
	own = org + "/" + repo
	for _, r := range repos {
		if strings.EqualFold(own, r) {
			return true, own
		}
	}
	return false, own
}

// NoCheckoutError is Root finding no known checkout of a plan's repos.
type NoCheckoutError struct{ Repos []string }

// Error names the plan's repos and says to run chunk watch once in a
// checkout of one.
func (e *NoCheckoutError) Error() string {
	if len(e.Repos) == 1 {
		r := e.Repos[0]
		return fmt.Sprintf("plan builds in %s, and chunk knows no checkout of it; run chunk watch once in a checkout of %s", r, r)
	}
	return fmt.Sprintf("plan builds in one of %s, and chunk knows no checkout of them; run chunk watch once in a checkout of one",
		strings.Join(e.Repos, ", "))
}

// Root is the project plan planID, recording repos, is planned and built
// in. It is prefer when repos is empty or prefer is a checkout of one of
// them. Otherwise it is one of known that is: the first holding planID's
// worktree, else the first. known is most recent first, and roots under
// chunk's data dir (plan worktrees) or whose origin is not on GitHub or
// cannot be read are skipped. No match is a *NoCheckoutError.
func Root(ctx context.Context, planID string, repos []string, prefer string, known []string) (string, error) {
	if len(repos) == 0 {
		return prefer, nil
	}
	if prefer != "" {
		if ok, _ := Matches(ctx, prefer, repos); ok {
			return prefer, nil
		}
	}
	base := ""
	if data, err := config.AppData(); err == nil {
		base = config.CanonicalProjectRoot(data) + string(filepath.Separator)
	}
	var first string
	for _, root := range known {
		if base != "" && strings.HasPrefix(config.CanonicalProjectRoot(root), base) {
			continue
		}
		if ok, _ := Matches(ctx, root, repos); !ok {
			continue
		}
		if holds(root, planID) {
			return root, nil
		}
		if first == "" {
			first = root
		}
	}
	if first == "" {
		return "", &NoCheckoutError{Repos: repos}
	}
	return first, nil
}

// holds reports whether planID's worktree exists for the repository at root.
func holds(root, planID string) bool {
	wt, err := factory.PlanWorktreePath(root, planID)
	if err != nil {
		return false
	}
	_, err = os.Stat(wt)
	return err == nil
}

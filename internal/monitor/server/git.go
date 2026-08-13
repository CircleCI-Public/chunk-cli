package server

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const (
	gitStatusClean    = "clean"
	gitStatusConflict = "conflict"
)

// startGitChecker runs a background loop that checks git upstream status for
// active sessions every two minutes.
func startGitChecker(ctx context.Context, db *sql.DB) {
	updateGitStatuses(ctx, db)
	go func() {
		ticker := time.NewTicker(2 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				updateGitStatuses(ctx, db)
			}
		}
	}()
}

func updateGitStatuses(ctx context.Context, db *sql.DB) {
	sessions, err := sessionsWithDir(ctx, db)
	if err != nil {
		log.Printf("git checker: list sessions: %v", err)
		return
	}
	log.Printf("git checker: checking %d session(s)", len(sessions))
	for _, s := range sessions {
		status := repoStatus(s.ProjectDir)
		log.Printf("git checker: %s → %q", s.ProjectDir, status)
		if err := setGitStatus(ctx, db, s.ID, status); err != nil {
			log.Printf("git checker: set git status: %v", err)
		}
		if status == gitStatusConflict {
			maybeDispatchResolver(ctx, db, s.ID, s.ProjectDir)
		}
	}
}

// repoStatus inspects the git repository at dir and returns a compact status string:
//
//	""        not a git repo or git unavailable
//	gitStatusClean   on a tracking branch, in sync, no uncommitted changes
//	"dirty"   uncommitted changes, no upstream divergence
//	"↑N"      N commits ahead of upstream
//	"↓N"      N commits behind upstream
//	"↑N↓M"    diverged: N ahead and M behind
//
// Dirty working tree is always noted in addition to ahead/behind, e.g. "↓2 dirty".
func repoStatus(dir string) string {
	if dir == "" {
		return ""
	}

	// Verify it's a git repo.
	if err := gitCmd(dir, "rev-parse", "--git-dir").Run(); err != nil {
		return ""
	}

	dirty := isWorkingTreeDirty(dir)

	ahead, behind, hasUpstream := upstreamDivergence(dir)
	if !hasUpstream {
		if dirty {
			return "dirty"
		}
		return gitStatusClean
	}

	// Check for merge conflicts before reporting ahead/behind counts.
	if behind > 0 && hasConflictsWithUpstream(dir) {
		return gitStatusConflict
	}

	var parts []string
	switch {
	case ahead > 0 && behind > 0:
		parts = append(parts, fmt.Sprintf("↑%d↓%d", ahead, behind))
	case ahead > 0:
		parts = append(parts, fmt.Sprintf("↑%d", ahead))
	case behind > 0:
		parts = append(parts, fmt.Sprintf("↓%d", behind))
	}
	if dirty {
		parts = append(parts, "dirty")
	}
	if len(parts) == 0 {
		return gitStatusClean
	}
	return strings.Join(parts, " ")
}

// hasConflictsWithUpstream uses git merge-tree to simulate merging the upstream
// into HEAD without touching the working tree or index. Returns true if the
// simulated merge would produce conflict markers.
func hasConflictsWithUpstream(dir string) bool {
	ref, ok := upstreamRef(dir)
	if !ok {
		return false
	}
	baseOut, err := gitCmd(dir, "merge-base", "HEAD", ref).Output()
	if err != nil {
		return false
	}
	base := strings.TrimSpace(string(baseOut))
	out, err := gitCmd(dir, "merge-tree", base, "HEAD", ref).Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "<<<<<<<")
}

func isWorkingTreeDirty(dir string) bool {
	out, err := gitCmd(dir, "status", "--porcelain").Output()
	if err != nil {
		return false
	}
	return len(strings.TrimSpace(string(out))) > 0
}

// upstreamRef returns the best available upstream ref for the given repo:
// the configured tracking branch if set, otherwise origin/HEAD as a fallback.
func upstreamRef(dir string) (string, bool) {
	out, err := gitCmd(dir, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}").Output()
	if err == nil {
		if ref := strings.TrimSpace(string(out)); ref != "" && ref != "@{upstream}" {
			return ref, true
		}
	}
	// No tracking branch — fall back to origin/HEAD (the remote's default branch).
	if err := gitCmd(dir, "rev-parse", "--verify", "origin/HEAD").Run(); err == nil {
		return "origin/HEAD", true
	}
	return "", false
}

func upstreamDivergence(dir string) (ahead, behind int, ok bool) {
	ref, ok := upstreamRef(dir)
	if !ok {
		return 0, 0, false
	}
	out, err := gitCmd(dir, "rev-list", "--left-right", "--count", "HEAD..."+ref).Output()
	if err != nil {
		return 0, 0, false
	}
	parts := strings.Fields(strings.TrimSpace(string(out)))
	if len(parts) != 2 {
		return 0, 0, false
	}
	ahead, _ = strconv.Atoi(parts[0])
	behind, _ = strconv.Atoi(parts[1])
	return ahead, behind, true
}

func gitCmd(dir string, args ...string) *exec.Cmd {
	return exec.Command("git", append([]string{"-C", dir}, args...)...)
}

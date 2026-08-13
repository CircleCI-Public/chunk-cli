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

const gitStatusClean = "clean"

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
	sessions, err := activeSessions(ctx, db)
	if err != nil {
		log.Printf("git checker: list sessions: %v", err)
		return
	}
	for _, s := range sessions {
		status := repoStatus(s.ProjectDir)
		if err := setGitStatus(ctx, db, s.ID, status); err != nil {
			log.Printf("git checker: set git status: %v", err)
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

func isWorkingTreeDirty(dir string) bool {
	out, err := gitCmd(dir, "status", "--porcelain").Output()
	if err != nil {
		return false
	}
	return len(strings.TrimSpace(string(out))) > 0
}

func upstreamDivergence(dir string) (ahead, behind int, ok bool) {
	out, err := gitCmd(dir, "rev-list", "--left-right", "--count", "HEAD...@{upstream}").Output()
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

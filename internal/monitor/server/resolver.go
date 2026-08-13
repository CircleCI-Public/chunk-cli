package server

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/CircleCI-Public/chunk-cli/internal/anthropic"
)

const (
	resolverModel     = "claude-sonnet-4-6"
	resolverMaxTokens = 8192
	// fileContentLimit caps each conflicted file passed to Claude.
	fileContentLimit = 16000
)

// maybeDispatchResolver fires a one-shot conflict resolver goroutine when the
// session's conflict base SHA has changed. It is a no-op when the conflict was
// already resolved for the current merge base.
func maybeDispatchResolver(ctx context.Context, db *sql.DB, sessionID, dir string) {
	ref, ok := upstreamRef(dir)
	if !ok {
		return
	}
	baseOut, err := gitCmd(dir, "merge-base", "HEAD", ref).Output()
	if err != nil {
		return
	}
	base := strings.TrimSpace(string(baseOut))
	if base == "" || getConflictBaseSHA(ctx, db, sessionID) == base {
		return
	}
	go func() {
		if err := resolveConflicts(ctx, db, sessionID, dir, ref, base); err != nil {
			log.Printf("resolver: %v", err)
		}
	}()
}

func resolveConflicts(ctx context.Context, db *sql.DB, sessionID, dir, ref, base string) error {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		return fmt.Errorf("ANTHROPIC_API_KEY not set")
	}
	client, err := anthropic.New(anthropic.Config{APIKey: apiKey})
	if err != nil {
		return fmt.Errorf("create client: %w", err)
	}

	localBranch := "HEAD"
	if out, err := gitCmd(dir, "rev-parse", "--abbrev-ref", "HEAD").Output(); err == nil {
		localBranch = strings.TrimSpace(string(out))
	}

	resolutionBranch := resolutionBranchName(localBranch)
	log.Printf("resolver: creating branch %s in %s", resolutionBranch, dir)

	// Create a temporary worktree so we can run the merge without disturbing
	// the original working tree.
	tmpDir, err := os.MkdirTemp("", "chunk-resolve-*")
	if err != nil {
		return fmt.Errorf("mktemp: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// New branch starting from HEAD, checked out in the temp worktree.
	if out, err := gitCmd(dir, "worktree", "add", "-b", resolutionBranch, tmpDir, "HEAD").CombinedOutput(); err != nil {
		return fmt.Errorf("worktree add: %w\n%s", err, out)
	}
	defer func() {
		_ = gitCmd(dir, "worktree", "remove", "--force", tmpDir).Run()
	}()

	// Start the merge without committing so conflict markers land in files.
	mergeOut, err := gitCmd(tmpDir, "merge", "--no-commit", "--no-ff", ref).CombinedOutput()
	if err == nil {
		// Clean merge — no conflicts to resolve, abort and clean up.
		_ = gitCmd(tmpDir, "merge", "--abort").Run()
		return nil
	}
	// A non-zero exit from merge --no-commit is expected when there are conflicts.
	log.Printf("resolver: merge produced conflicts: %s", strings.TrimSpace(string(mergeOut)))

	// Find all files with conflict markers.
	conflicted, err := conflictedFiles(tmpDir)
	if err != nil {
		return err
	}
	if len(conflicted) == 0 {
		_ = gitCmd(tmpDir, "merge", "--abort").Run()
		return nil
	}

	log.Printf("resolver: asking Claude to resolve %d file(s)", len(conflicted))
	for _, path := range conflicted {
		if err := resolveFile(ctx, client, tmpDir, path, localBranch, ref); err != nil {
			log.Printf("resolver: %s: %v", path, err)
		}
	}

	// Commit the resolution.
	msg := fmt.Sprintf("chunk: resolve conflicts between %s and %s\n\nResolved by chunk monitor using %s.",
		localBranch, ref, resolverModel)
	if out, err := gitCmd(tmpDir, "commit", "--no-edit", "-m", msg).CombinedOutput(); err != nil {
		return fmt.Errorf("commit: %w\n%s", err, out)
	}

	if err := setConflictBaseSHA(ctx, db, sessionID, base); err != nil {
		log.Printf("resolver: save base sha: %v", err)
	}
	log.Printf("resolver: branch %q is ready — merge it to apply the resolution", resolutionBranch)
	return nil
}

// resolveFile asks Claude to resolve all conflict markers in path, writes the
// resolved content back, and stages the file.
func resolveFile(ctx context.Context, client *anthropic.Client, dir, path, localBranch, ref string) error {
	fullPath := filepath.Join(dir, path)
	raw, err := os.ReadFile(fullPath)
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}
	content := string(raw)
	if !strings.Contains(content, "<<<<<<<") {
		// File was auto-merged cleanly; just stage it.
		if out, err := gitCmd(dir, "add", path).CombinedOutput(); err != nil {
			return fmt.Errorf("add: %w\n%s", err, out)
		}
		return nil
	}
	if len(content) > fileContentLimit {
		content = content[:fileContentLimit] + "\n... (truncated)"
	}

	prompt := fmt.Sprintf(
		"You are resolving a Git merge conflict in the file %q.\n"+
			"The conflict is between branch %q (<<<<<<< HEAD) and %q (>>>>>>> ...).\n\n"+
			"Return ONLY the fully resolved file content with all conflict markers removed.\n"+
			"Do not include any explanation, markdown fencing, or extra text — just the file content.\n\n"+
			"%s",
		path, localBranch, ref, content)

	resolved, err := client.Ask(ctx, resolverModel, resolverMaxTokens, prompt)
	if err != nil {
		return fmt.Errorf("ask claude: %w", err)
	}

	if err := os.WriteFile(fullPath, []byte(resolved), 0o644); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	if out, err := gitCmd(dir, "add", path).CombinedOutput(); err != nil {
		return fmt.Errorf("add: %w\n%s", err, out)
	}
	return nil
}

func conflictedFiles(dir string) ([]string, error) {
	out, err := gitCmd(dir, "diff", "--name-only", "--diff-filter=U").Output()
	if err != nil {
		return nil, fmt.Errorf("diff unmerged: %w", err)
	}
	var files []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			files = append(files, line)
		}
	}
	return files, nil
}

func resolutionBranchName(localBranch string) string {
	// Sanitise branch name: replace slashes so the prefix stays readable.
	safe := strings.ReplaceAll(localBranch, "/", "-")
	return "chunk/resolve-" + safe
}

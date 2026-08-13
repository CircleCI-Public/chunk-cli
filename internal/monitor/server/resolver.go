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
	// mergeTreeLimit caps the merge-tree output passed to Claude.
	mergeTreeLimit = 32000
)

// maybeDispatchResolver fires a one-shot conflict resolver goroutine when the
// session's conflict base SHA has changed — meaning either a new conflict was
// detected or the upstream moved. It is a no-op when the conflict was already
// analysed for the current merge base.
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

	mergeOut, err := gitCmd(dir, "merge-tree", base, "HEAD", ref).Output()
	if err != nil {
		return fmt.Errorf("merge-tree: %w", err)
	}
	mergeContent := string(mergeOut)
	if !strings.Contains(mergeContent, "<<<<<<<") {
		return nil
	}
	if len(mergeContent) > mergeTreeLimit {
		mergeContent = mergeContent[:mergeTreeLimit] + "\n... (truncated)"
	}

	localBranch := "HEAD"
	if out, err := gitCmd(dir, "rev-parse", "--abbrev-ref", "HEAD").Output(); err == nil {
		localBranch = strings.TrimSpace(string(out))
	}

	log.Printf("resolver: dispatching Claude to resolve conflicts in %s", dir)
	prompt := buildResolverPrompt(localBranch, ref, mergeContent)
	resolution, err := client.Ask(ctx, resolverModel, resolverMaxTokens, prompt)
	if err != nil {
		return fmt.Errorf("ask claude: %w", err)
	}

	if err := writeConflictContext(dir, localBranch, ref, resolution); err != nil {
		return fmt.Errorf("write context: %w", err)
	}
	if err := setConflictBaseSHA(ctx, db, sessionID, base); err != nil {
		return fmt.Errorf("save base sha: %w", err)
	}
	log.Printf("resolver: wrote conflict resolution to %s/.chunk/context/conflicts.md", dir)
	return nil
}

func buildResolverPrompt(localBranch, upstreamRef, mergeTree string) string {
	return fmt.Sprintf(
		"You are a Git conflict resolver. The repository has merge conflicts between"+
			" the local branch %q and upstream %q.\n\n"+
			"Below is the output of `git merge-tree`, which shows the conflicting sections"+
			" using standard conflict markers (<<<<<<< .our / ======= / >>>>>>> .their).\n\n"+
			"Your task:\n"+
			"1. Identify each conflicting file and section.\n"+
			"2. For each conflict, explain in one sentence WHY the conflict exists (what changed on each side).\n"+
			"3. Provide the resolved content for that section — choose the resolution that best preserves"+
			" both intents, or clearly explain the trade-off if they are mutually exclusive.\n"+
			"4. End with a short summary of all changes needed so a developer can apply them quickly.\n\n"+
			"Format your response as Markdown. Use fenced code blocks for all code.\n\n"+
			"<merge-tree-output>\n%s\n</merge-tree-output>",
		localBranch, upstreamRef, mergeTree)
}

func writeConflictContext(dir, localBranch, upstreamRef, resolution string) error {
	contextDir := filepath.Join(dir, ".chunk", "context")
	if err := os.MkdirAll(contextDir, 0o755); err != nil {
		return fmt.Errorf("mkdir context: %w", err)
	}
	content := fmt.Sprintf("# Conflict Resolution\n\n**Branch:** `%s`  \n**Conflicts with:** `%s`\n\n%s\n",
		localBranch, upstreamRef, resolution)
	return os.WriteFile(filepath.Join(contextDir, "conflicts.md"), []byte(content), 0o644)
}

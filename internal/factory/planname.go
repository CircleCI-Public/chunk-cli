package factory

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// namePrompt is the system prompt NamePlan gives claude.
const namePrompt = `You name plans for code changes. Read the developer's request and reply with a name for the change they are aiming for: 2 or 3 lowercase words joined by hyphens, the way a git branch is named, such as agent-chosen-plan-names or retry-flaky-uploads. Name the outcome they want; do not copy the first words of their message. Reply with the name alone: no quotes, punctuation or explanation.`

// nameModel is the model NamePlan names with. A name is a small task, and
// the developer's first message waits on it.
const nameModel = "haiku"

// nameTimeout caps how long NamePlan waits on claude. A package variable so
// tests can shorten it.
var nameTimeout = 20 * time.Second

// NamePlan returns claude's name for a plan for message, the developer's
// request, trimmed but otherwise as claude gave it. Claude sees only the
// message. ErrNoClaude is returned when claude is not installed.
func NamePlan(ctx context.Context, message string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, nameTimeout)
	defer cancel()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, claudeBinary,
		"-p", "--model", nameModel, "--output-format", "text",
		"--no-session-persistence", "--tools", "", "--strict-mcp-config",
		"--system-prompt", namePrompt,
	)
	// Run outside the repository so its CLAUDE.md and project settings are
	// not loaded into a call that only reads the message.
	cmd.Dir = os.TempDir()
	cmd.Stdin = strings.NewReader(message)
	cmd.Stdout = &stdout
	cmd.Stderr = &limitWriter{buf: &stderr}
	stopTree(cmd)
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	switch {
	case errors.Is(err, exec.ErrNotFound):
		return "", ErrNoClaude
	case ctx.Err() != nil:
		return "", fmt.Errorf("name the plan: %w", ctx.Err())
	case err != nil:
		return "", fmt.Errorf("name the plan: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

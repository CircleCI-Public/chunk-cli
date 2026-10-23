package factory

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/claudecode"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
)

// planTimeout bounds one planning turn. A turn reads code and rewrites the
// plan; it does not build or test anything.
const planTimeout = 15 * time.Minute

// planSystemPrompt has Claude keep the plan in one file, which the developer
// reads beside the chat, and talk about it in its replies. The format is
// factory's (plugin/planning.md in circleci/factory), so a plan made here can
// be worked on by factory's tasks.
const planSystemPrompt = `You are planning a change with a developer, in their repository. You do not make the change: the plan you write is handed to an implementer agent that does.

Keep the plan in the file %s, and only that file: create it on your first turn, and edit it as the conversation goes. Never edit any other file. Read the code as much as you need to make the plan specific.

The plan starts with a "# " title line naming the change in a few lowercase words joined by hyphens. Then it has these sections, as markdown headings with exactly these names:

## 1. Outcome
1-2 sentences: at a high level, what is true when this is done that is not true now.
## 2. Design
Four subsections, each a ### heading with exactly this name, so each can be read and edited on its own:
### Context
Only load-bearing facts: what the Spec, Decisions or Units depend on that a reader new to the code would get wrong or not know. Give each its reason (what in the plan breaks if it is false) and how you verified it.
### Spec
User-facing behaviors.
### Decisions
Choices between alternatives, ones the reader might have made differently. Mark each [USER] when the developer dictated it or [AGENT] when you proposed it, with tradeoffs and rejected alternatives as sub-bullets.
### Out of scope
What is not being done, and why.

## 3. Open Questions
Questions for the developer, one per bullet, with enough context to see the tradeoffs. Leave the body empty when there are none; never write "None.". Never list resolved questions.

## 4. Worked example
Walk one concrete case end to end through the finished change, showing every new or changed interface (signatures, types, config, CLI flags, payloads) as code snippets, before and after where something changes.

## 5. Units
Discrete units of work, each a ### heading such as "### Unit 1: parser", large enough to occupy a builder and small enough to acceptance test on its own. Each names the files it changes with load-bearing code snippets, its tests alongside (never in a separate unit), and the commands that verify it.

Each reply goes to the developer in a chat beside the plan. Keep it short: say what you changed in the plan and ask about anything you need answered. Do not repeat the plan in the reply.`

// PlanSession is a conversation with Claude Code, on the developer's machine,
// about a plan kept in a file. Its turns resume one claude session.
type PlanSession struct {
	// Dir is the repository Claude reads while planning.
	Dir string
	// PlanPath is the file Claude keeps the plan in.
	PlanPath string
	// Model is the model to plan with, or "" for the developer's default.
	Model string
	// SessionID is claude's session, "" until the first turn reports it.
	SessionID string
}

// Turn sends the developer's message to Claude and returns its reply. The plan
// is left in PlanPath; read it with Plan.
func (s *PlanSession) Turn(ctx context.Context, message string, onActivity func(claudecode.Activity)) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, planTimeout)
	defer cancel()

	st := claudecode.NewStream(onActivity)
	var stderr bytes.Buffer
	args := append(streamArgs(),
		"--permission-mode", "acceptEdits",
		// Claude only reads the code; the plan file is all it writes.
		flagDisallowedTools, "Bash",
		// The plan is kept outside the repository, where claude would
		// otherwise ask before writing.
		"--add-dir", filepath.Dir(s.PlanPath),
		flagAppendSystemPrompt, fmt.Sprintf(planSystemPrompt, s.PlanPath),
	)
	if s.SessionID != "" {
		args = append(args, "--resume", s.SessionID)
	}
	if s.Model != "" {
		args = append(args, "--model", s.Model)
	}
	cmd := exec.CommandContext(ctx, claudeBinary, args...)
	cmd.Dir = s.Dir
	cmd.Env = append(os.Environ(), config.EnvChunkHooksDisabled+"=1")
	cmd.Stdin = strings.NewReader(message)
	cmd.Stdout = streamWriter{st}
	cmd.Stderr = &limitWriter{buf: &stderr}
	stopTree(cmd)
	cmd.WaitDelay = 5 * time.Second
	runErr := cmd.Run()
	st.Flush()
	if id := st.SessionID(); id != "" {
		s.SessionID = id
	}
	res, sawResult := st.Result()

	switch {
	case errors.Is(runErr, exec.ErrNotFound):
		return "", ErrNoClaude
	case errors.Is(ctx.Err(), context.Canceled):
		return res.Result, ctx.Err()
	}
	code := 0
	if exitErr, ok := errors.AsType[*exec.ExitError](runErr); ok {
		code, runErr = exitErr.ExitCode(), nil
	}
	return res.Result, turnError(ctx, "planning", planTimeout, code, runErr, res, sawResult, stderr.String())
}

// Plan is the plan as Claude has written it so far, or "" before it has.
func (s *PlanSession) Plan() (string, error) {
	b, err := os.ReadFile(s.PlanPath)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read the plan: %w", err)
	}
	return string(b), nil
}

package factory

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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
// factory-bot's, so a plan made here reads as one made there.
const planSystemPrompt = `You are planning a change with a developer, in their repository. You do not make the change: the plan you write is handed to an implementer agent that does.

Keep the plan in the file %s, and only that file: create it on your first turn, and edit it as the conversation goes. Never edit any other file. Read the code as much as you need to make the plan specific.

You may run read-only git and gh commands to look at other branches and pull requests, for example git fetch origin, git show origin/<branch>:<path>, git diff main...origin/<branch> and gh pr diff <number>. Never change the working tree: do not check out, switch, reset, stash, commit or push, and do not comment on pull requests.

The plan has these sections, as markdown headings:

## 1. Outcome
1-2 sentences: what is true when this is done that is not true now.

## 2. Spec/Decisions/Scope
Spec: user facing behaviors. Decisions: load-bearing, non-obvious implementation details, each marked [USER] when the developer dictated it or [AGENT] when you proposed it, with tradeoffs and rejected alternatives as sub-bullets. Out of scope: what is not being done, and why.

## 3. Open Questions
Questions for the developer, one per bullet, with enough context to see the tradeoffs. Leave the body empty when there are none; never write "None.". Never list resolved questions.

## 4. Worked example
Walk one concrete case end to end through the finished change, showing every new or changed interface (signatures, types, config, CLI flags, payloads) as code snippets.

## 5. Units
Discrete units of work, each naming the files it changes with load-bearing code snippets, its tests alongside, and the commands that verify it.

Each reply goes to the developer in a chat beside the plan. Keep it short: say what you changed in the plan and ask about anything you need answered. Do not repeat the plan in the reply.`

// planTools are the shell commands a planning turn may run. They read history
// and pull requests; none of them changes the working tree, and none runs a
// program of the developer's. A rule ending in :* allows any arguments, so it
// is only given to a command whose every flag is read-only (planDisallowed
// closes the few that are not). A rule without it is that command exactly:
// fetch updates only the remote-tracking branches, which is all planning needs
// to read a collaborator's branch, and the forms of branch are the listing ones.
var planTools = []string{
	"Bash(git log:*)", "Bash(git diff:*)", "Bash(git show:*)", "Bash(git status:*)",
	"Bash(git rev-parse:*)", "Bash(git ls-files:*)", "Bash(git ls-tree:*)",
	"Bash(git fetch origin)",
	"Bash(git branch)", "Bash(git branch -a)", "Bash(git branch -r)", "Bash(git branch -vv)", "Bash(git branch --show-current)",
	"Bash(gh pr view:*)", "Bash(gh pr diff:*)", "Bash(gh pr list:*)", "Bash(gh pr checks:*)",
}

// planDisallowed is what planning never does, over and above planTools being
// all it may run: a deny rule wins over an allow rule wherever it comes from.
// The first group are the flags that make an otherwise read-only git command
// write a file or run a program. A * in the middle of a rule matches wherever
// the flag is in the command, which a :* suffix does not.
var planDisallowed = append(slices.Clone(disallowedGit),
	"Bash(git * --output*)", "Bash(git * --upload-pack*)", "Bash(git * --receive-pack*)", "Bash(git * --exec*)",
	"Bash(git push:*)", "Bash(git merge:*)", "Bash(git pull:*)", "Bash(git clean:*)", "Bash(git restore:*)",
	"Bash(gh pr comment:*)", "Bash(gh pr merge:*)", "Bash(gh pr review:*)", "Bash(gh pr close:*)",
	"Bash(gh pr edit:*)", "Bash(gh pr create:*)", "Bash(gh api:*)",
)

// planEditRule is the one file planning may write. // makes the path absolute
// in a permission rule.
func planEditRule(planPath string) string {
	return "Edit(//" + strings.TrimPrefix(filepath.ToSlash(planPath), "/") + ")"
}

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
		// Claude reads the code and other branches; the plan file is all
		// it writes. Edits are allowed to that file alone rather than by
		// acceptEdits, which would also let it touch, move and remove files.
		flagAllowedTools, strings.Join(append(slices.Clone(planTools), planEditRule(s.PlanPath)), ","),
		flagDisallowedTools, strings.Join(planDisallowed, ","),
		// Only these rules apply: the project's and the developer's own allow
		// rules, which `chunk init` adds Bash(chunk:*) to, are not the
		// planner's to use. Their hooks do not run either.
		flagSettingSources, "",
		// The plan is kept outside the repository, where claude would
		// otherwise ask before reading it.
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
	// git must not wait for a password the planning turn cannot give.
	cmd.Env = append(os.Environ(), config.EnvChunkHooksDisabled+"=1", "GIT_TERMINAL_PROMPT=0")
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

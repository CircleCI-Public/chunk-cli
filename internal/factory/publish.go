package factory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/CircleCI-Public/chunk-cli/internal/gitexec"
	"github.com/CircleCI-Public/chunk-cli/internal/gitremote"
)

// DefaultRemote is the remote a run's branch is published to.
const DefaultRemote = "origin"

// PublishMode is how a finished run's branch is published.
type PublishMode string

// The ways to publish. Explicit modes never prompt.
const (
	// PublishPrompt asks, in a terminal, after a run that passed.
	PublishPrompt PublishMode = "prompt"
	// PublishPush pushes the branch without asking.
	PublishPush PublishMode = "push"
	// PublishPR pushes the branch and opens a PR without asking.
	PublishPR PublishMode = "pr"
	// PublishOff leaves the branch local.
	PublishOff PublishMode = "off"
)

// PublishAction is what publishing does to the remote.
type PublishAction string

// The actions a publish decision can come to.
const (
	// ActionNone publishes nothing.
	ActionNone PublishAction = ""
	// ActionPush pushes the branch only.
	ActionPush PublishAction = "push"
	// ActionOpenPR pushes the branch and opens a PR.
	ActionOpenPR PublishAction = "open-pr"
	// ActionUpdatePR pushes to the branch of an existing PR.
	ActionUpdatePR PublishAction = "update-pr"
)

// PublishFacts is what deciding whether to publish depends on.
type PublishFacts struct {
	Mode        PublishMode
	Committed   bool
	Passed      bool
	HasRemote   bool
	HasPR       bool
	Interactive bool
}

// PublishDecision is what to do about publishing. Reason says why nothing is
// done when Action is ActionNone, or "" when that needs no explaining.
type PublishDecision struct {
	Action PublishAction
	// Ask reports that the user is asked before the action is taken.
	Ask    bool
	Reason string
}

// DecidePublish settles whether and how to publish. An explicit mode wins over
// the prompt, which is only for a passed run in a terminal; anything else
// leaves the branch local.
func DecidePublish(f PublishFacts) PublishDecision {
	switch {
	case !f.Committed:
		return PublishDecision{Reason: "nothing was committed, so there is nothing to publish"}
	case f.Mode == PublishOff:
		return PublishDecision{}
	case !f.HasRemote:
		return PublishDecision{Reason: "the repository has no " + DefaultRemote + " remote, so the work stays on its local branch"}
	}
	switch f.Mode {
	case PublishPush:
		return PublishDecision{Action: ActionPush}
	case PublishPR:
		if f.HasPR {
			return PublishDecision{Action: ActionUpdatePR}
		}
		return PublishDecision{Action: ActionOpenPR}
	case PublishPrompt, PublishOff:
	}
	if !f.Passed {
		return PublishDecision{Reason: "the run's checks did not all pass, so it is not offered for publishing"}
	}
	if !f.Interactive {
		return PublishDecision{}
	}
	if f.HasPR {
		return PublishDecision{Action: ActionUpdatePR, Ask: true}
	}
	return PublishDecision{Action: ActionOpenPR, Ask: true}
}

// CheckHeadBranch refuses a head branch that is not a factory branch or is
// the default branch. Nothing but a chunk/factory/* branch is ever pushed.
func CheckHeadBranch(head, defaultBranch string) error {
	switch {
	case !strings.HasPrefix(head, branchPrefix):
		return fmt.Errorf("refusing to publish %s: it is not a %s* branch", head, branchPrefix)
	case head == defaultBranch:
		return fmt.Errorf("refusing to publish %s: it is the default branch", head)
	}
	return nil
}

// ChooseBase is the branch a PR is opened into: the override, else the branch
// the run started from when that is a real integration branch, else the
// default. A run started from a detached HEAD or another factory branch has no
// such branch of its own.
func ChooseBase(override, runBase, defaultBranch string) string {
	if override != "" {
		return override
	}
	if runBase != "" && !strings.HasPrefix(runBase, branchPrefix) {
		return runBase
	}
	return defaultBranch
}

// PR is a pull request on the host.
type PR struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
}

// PRCreate is a pull request to open.
type PRCreate struct {
	Base, Head, Title, Body string
	Draft                   bool
}

// PRHost looks up and opens pull requests. It is what the GitHub CLI provides
// here, and a seam for tests.
type PRHost interface {
	// Existing is the open PR from head, or nil.
	Existing(ctx context.Context, head string) (*PR, error)
	// Create opens a PR and returns its URL.
	Create(ctx context.Context, pr PRCreate) (string, error)
}

// GHHost is a PRHost backed by the GitHub CLI, run in Dir.
type GHHost struct{ Dir string }

// NewGHHost returns a host for the repository at dir, or nil when gh is not
// on PATH.
func NewGHHost(dir string) *GHHost {
	if _, err := exec.LookPath("gh"); err != nil {
		return nil
	}
	return &GHHost{Dir: dir}
}

func (h GHHost) run(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Dir = h.Dir
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return nil, fmt.Errorf("gh %s: %w: %s", args[0]+" "+args[1], err, strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("gh %s: %w", args[0]+" "+args[1], err)
	}
	return out, nil
}

// Existing implements PRHost.
func (h GHHost) Existing(ctx context.Context, head string) (*PR, error) {
	out, err := h.run(ctx, "pr", "list", "--head", head, "--state", "open", "--json", "number,url", "--limit", "1")
	if err != nil {
		return nil, err
	}
	var prs []PR
	if err := json.Unmarshal(out, &prs); err != nil {
		return nil, fmt.Errorf("read gh's pull request list: %w", err)
	}
	if len(prs) == 0 {
		return nil, nil
	}
	return &prs[0], nil
}

// Create implements PRHost.
func (h GHHost) Create(ctx context.Context, pr PRCreate) (string, error) {
	args := []string{"pr", "create", "--base", pr.Base, "--head", pr.Head, "--title", pr.Title, "--body", pr.Body}
	if pr.Draft {
		args = append(args, "--draft")
	}
	out, err := h.run(ctx, args...)
	if err != nil {
		return "", err
	}
	lines := strings.Fields(string(out))
	if len(lines) == 0 {
		return "", errors.New("gh pr create printed no URL")
	}
	return lines[len(lines)-1], nil
}

// Publication is a run's branch being published from its worktree.
type Publication struct {
	// Dir is the run's worktree.
	Dir    string
	Remote string
	Head   string
	// BaseBranch and DefaultBranch are the run's, see Worktree. Base, when
	// set, overrides the choice of PR base.
	BaseBranch, DefaultBranch, Base string
	Title, Body                     string
	Draft                           bool
	// Host looks up and opens PRs; nil when none is available.
	Host PRHost
}

// PublishTarget is where a publication goes: the base it targets and the PR
// it already has.
type PublishTarget struct {
	Base     string
	Existing *PR
	// LookupErr says why an existing PR could not be looked up. Pushing is
	// still safe: it updates a PR if there is one.
	LookupErr error
}

// Resolve applies the branch guard and works out the PR's base and whether the
// branch already has a PR.
func (p Publication) Resolve(ctx context.Context) (PublishTarget, error) {
	if err := CheckHeadBranch(p.Head, p.DefaultBranch); err != nil {
		return PublishTarget{}, err
	}
	base := ChooseBase(p.Base, p.BaseBranch, p.DefaultBranch)
	if p.Base == "" && base != p.DefaultBranch && !p.onRemote(ctx, base) {
		// A local feature branch the remote has never seen cannot be a PR's base.
		base = p.DefaultBranch
	}
	if base == "" {
		return PublishTarget{}, errors.New("cannot tell which branch to open the PR into: the repository records no default branch")
	}
	if base == p.Head {
		return PublishTarget{}, fmt.Errorf("refusing to open a PR from %s into itself", base)
	}
	t := PublishTarget{Base: base}
	if p.Host != nil {
		t.Existing, t.LookupErr = p.Host.Existing(ctx, p.Head)
	}
	return t, nil
}

func (p Publication) onRemote(ctx context.Context, branch string) bool {
	return (gitexec.Runner{Dir: p.Dir}).Run(ctx, "rev-parse", "--verify", "--quiet", "refs/remotes/"+p.Remote+"/"+branch) == nil
}

// Push pushes the head branch, and only it, to the remote under its own name.
// It never forces.
func (p Publication) Push(ctx context.Context) error {
	if err := CheckHeadBranch(p.Head, p.DefaultBranch); err != nil {
		return err
	}
	ref := "refs/heads/" + p.Head
	out, err := (gitexec.Runner{Dir: p.Dir}).CombinedOutput(ctx, "push", "--set-upstream", p.Remote, ref+":"+ref)
	if err != nil {
		return fmt.Errorf("push %s to %s: %w: %s", p.Head, p.Remote, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// OpenPR opens a PR from the head branch into t.Base, which must be pushed.
func (p Publication) OpenPR(ctx context.Context, t PublishTarget) (string, error) {
	if p.Host == nil {
		return "", errors.New("the GitHub CLI (gh) is not installed")
	}
	return p.Host.Create(ctx, PRCreate{Base: t.Base, Head: p.Head, Title: p.Title, Body: p.Body, Draft: p.Draft})
}

// CompareURL is the page on GitHub for opening a PR by hand, or "" when the
// remote is not on GitHub.
func (p Publication) CompareURL(ctx context.Context, t PublishTarget) string {
	url, err := gitremote.URL(ctx, p.Dir, p.Remote)
	if err != nil {
		return ""
	}
	org, repo, err := gitremote.ParseRemoteURL(url)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("https://github.com/%s/%s/compare/%s...%s?expand=1", org, repo, t.Base, p.Head)
}

// PRTitle is the PR's default title: the request's first line.
func PRTitle(prompt string) string {
	subject, _, _ := strings.Cut(strings.TrimSpace(prompt), "\n")
	return strings.TrimSpace(subject)
}

// PRBody is the PR's default description: the request and how the run ended.
func PRBody(prompt, runID, result string, rounds int, stat string) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(prompt))
	b.WriteString("\n\n---\n")
	fmt.Fprintf(&b, "Written by chunk factory run %s", runID)
	if result != "" {
		fmt.Fprintf(&b, ": %s after %d round(s)", result, rounds)
	}
	b.WriteString(".\n")
	if stat != "" {
		fmt.Fprintf(&b, "\n%s\n", stat)
	}
	return b.String()
}

package factory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/smeltdev/smelt/internal/harness"
	"github.com/smeltdev/smelt/internal/review"
)

const (
	// planTimeout bounds one attempt at writing a plan.
	planTimeout = 5 * time.Minute
	// maxPlanOutputBytes caps the plan agent's result.
	maxPlanOutputBytes = 128 * 1024
	// planFile is the path a plan review gives a finding about the plan as a
	// whole; findings need a repository path, and the plan has none.
	planFile = "plan"
)

// planTools limits the plan writer to reading the repository.
var planTools = []string{"Read", "Grep", "Glob", "Bash(git log:*)", "Bash(git diff:*)"}

// PlanSchema is the JSON Schema of the plan the implementer writes before it
// writes code. It is passed with --json-schema (--output-schema for Codex), so
// the agent's CLI validates the answer itself.
const PlanSchema = `{
  "type": "object",
  "properties": {
    "summary": {"type": "string", "description": "2-4 sentences: the approach."},
    "files": {
      "type": "array",
      "description": "Every file the change touches.",
      "items": {
        "type": "object",
        "properties": {
          "path": {"type": "string"},
          "change": {"type": "string", "description": "add, modify, delete or new package."},
          "why": {"type": "string"}
        },
        "required": ["path", "change", "why"],
        "additionalProperties": false
      }
    },
    "acceptance": {
      "type": "array",
      "description": "One entry per requirement and acceptance item of the request.",
      "items": {
        "type": "object",
        "properties": {
          "item": {"type": "string", "description": "The acceptance line, quoted."},
          "how": {"type": "string", "description": "How the change will meet it."},
          "verifiable_in_sandbox": {"type": "boolean"}
        },
        "required": ["item", "how", "verifiable_in_sandbox"],
        "additionalProperties": false
      }
    },
    "risks": {"type": "array", "items": {"type": "string"}},
    "questions": {"type": "array", "items": {"type": "string"}, "description": "Decisions the request leaves open. Answer each with the most reasonable choice and say so."},
    "cannot_verify_here": {"type": "array", "items": {"type": "string"}, "description": "What cannot be verified in the sandbox, such as needing Docker on the host."}
  },
  "required": ["summary", "files", "acceptance", "risks", "questions", "cannot_verify_here"],
  "additionalProperties": false
}`

// Plan is what the implementer says it will do before it does it.
type Plan struct {
	Summary          string     `json:"summary"`
	Files            []PlanFile `json:"files"`
	Acceptance       []PlanItem `json:"acceptance"`
	Risks            []string   `json:"risks"`
	Questions        []string   `json:"questions"`
	CannotVerifyHere []string   `json:"cannot_verify_here"`
}

// PlanFile is one file a plan touches.
type PlanFile struct {
	Path   string `json:"path"`
	Change string `json:"change"`
	Why    string `json:"why"`
}

// PlanItem is how a plan meets one acceptance item of the request.
type PlanItem struct {
	Item                string `json:"item"`
	How                 string `json:"how"`
	VerifiableInSandbox bool   `json:"verifiable_in_sandbox"`
}

// PlanInput is what a plan is asked for: the request, and when the plan is a
// revision, the plan under revision and what its review found wrong with it.
type PlanInput struct {
	Request  string
	Previous *Plan
	Findings []review.Finding
}

// Planner writes the plan a run implements and has it reviewed. Sidecars
// implements it; tests implement it in memory.
type Planner interface {
	// Plan writes a plan for the request, or revises one.
	Plan(ctx context.Context, in PlanInput) (Plan, error)
	// ReviewPlan returns what the plan's reviewers found wrong with it.
	ReviewPlan(ctx context.Context, request string, plan Plan) ([]review.Finding, error)
}

// Markdown renders the plan for the agents that read it.
func (p Plan) Markdown() string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(p.Summary) + "\n")
	if len(p.Files) > 0 {
		b.WriteString("\nFiles:\n")
		for _, f := range p.Files {
			fmt.Fprintf(&b, "- %s (%s): %s\n", f.Path, f.Change, f.Why)
		}
	}
	if len(p.Acceptance) > 0 {
		b.WriteString("\nAcceptance:\n")
		for _, a := range p.Acceptance {
			verify := ""
			if !a.VerifiableInSandbox {
				verify = " [not verifiable in the sandbox]"
			}
			fmt.Fprintf(&b, "- %s: %s%s\n", a.Item, a.How, verify)
		}
	}
	writeList(&b, "Risks", p.Risks)
	writeList(&b, "Open decisions, as the plan settles them", p.Questions)
	writeList(&b, "Cannot be verified here", p.CannotVerifyHere)
	return strings.TrimRight(b.String(), "\n")
}

func writeList(b *strings.Builder, heading string, items []string) {
	if len(items) == 0 {
		return
	}
	fmt.Fprintf(b, "\n%s:\n", heading)
	for _, it := range items {
		fmt.Fprintf(b, "- %s\n", it)
	}
}

// planPrompt asks for a plan, or a revision of one.
func planPrompt(in PlanInput) string {
	var b strings.Builder
	b.WriteString("You are about to implement the change requested below in this repository. Before writing any code, write a short plan. ")
	b.WriteString("Read the repository as much as you need, but do not modify anything.\n\n")
	b.WriteString("The plan names the files you will touch, the approach, how each requirement and acceptance item of the request will be met ")
	b.WriteString("(one acceptance entry per item, quoting it), the risks, and what you cannot verify in a sandbox. ")
	b.WriteString("Where the request leaves a decision open, answer it with the most reasonable choice and say so in questions. ")
	b.WriteString("Base the plan on what the code does, not on what you expect it to do.\n\n")
	b.WriteString("## Requested change\n\n")
	b.WriteString(strings.TrimSpace(in.Request))
	b.WriteString("\n")
	if in.Previous != nil {
		b.WriteString("\n## Your previous plan\n\n")
		b.WriteString(in.Previous.Markdown())
		b.WriteString("\n\n## What its review found\n\n")
		b.WriteString(findingList(in.Findings))
		b.WriteString("\nWrite a revised plan that resolves these. If you disagree with a finding, keep the plan and say why in questions.\n")
	}
	return b.String()
}

// findingList renders findings as a markdown list.
func findingList(findings []review.Finding) string {
	var b strings.Builder
	for _, f := range findings {
		fmt.Fprintf(&b, "- [%s] %s: %s\n", f.Severity, f.Location(), f.Body)
	}
	return b.String()
}

// planReviewBody is the whole of a plan review's instructions. A plan review is
// not the diff review its prompt is written for, so the prompt's own text is
// left out: only the name says which reviewer asks.
const planReviewBody = `Review the plan below against the requested change. No code has been written, so there is no diff: do not look for one, and do not implement anything.

## Requested change

%s

## Plan

%s

## Review instructions

Answer only these questions:

1. Does the plan cover every requirement and acceptance item the request states?
2. Does any file choice contradict the request?
3. Is anything in the plan out of scope?

Report a high or medium finding only for a requirement or acceptance item the plan leaves out, or a choice that contradicts the request. Use ` + "`" + planFile + "`" + ` as the file for a finding about the plan as a whole, or the file the finding is about. Anything else is low.
Do not judge how the code will be written; judge only what the plan commits to.`

// planReviews are the prompts that review a plan: the reviews that judge a
// change against the whole request, when the project has them.
func planReviews(request string, plan Plan, prompts []review.Prompt) []review.Prompt {
	var out []review.Prompt
	for _, p := range prompts {
		if slices.Contains(completenessReviews, p.Name) {
			out = append(out, review.Prompt{Name: p.Name, Body: fmt.Sprintf(planReviewBody, strings.TrimSpace(request), plan.Markdown())})
		}
	}
	return out
}

// Plan asks the implementer's agent, which has the repository on its sidecar,
// for a plan. The agent may read the repository but not change it.
func (s *Sidecars) Plan(ctx context.Context, in PlanInput) (Plan, error) {
	im := s.Implementer
	prompt := planPrompt(in)
	h := im.agent()
	script := h.ReviewScript(harness.ReviewInput{
		RepoPath: im.Entry.RepoPath, Prompt: prompt, Model: im.Model, Schema: PlanSchema, Tools: planTools,
	})
	ctx, cancel := context.WithTimeout(ctx, planTimeout)
	defer cancel()
	out, err := review.RunScriptEnv(ctx, s.Exec, im.Entry, script, h.Env(im.Credential, im.BaseURL), maxPlanOutputBytes)
	if ctx.Err() == context.DeadlineExceeded {
		return Plan{}, fmt.Errorf("timed out after %s", planTimeout)
	}
	if err != nil {
		return Plan{}, err
	}
	if h.Name() != harness.Claude {
		return parsePlanMessage(out)
	}
	return ParsePlan(out)
}

// ReviewPlan runs the plan reviews once, each on a reviewer sidecar. A review
// that could not run says nothing against the plan, so it adds no findings.
func (s *Sidecars) ReviewPlan(ctx context.Context, request string, plan Plan) ([]review.Finding, error) {
	prompts := planReviews(request, plan, s.Prompts)
	if len(prompts) == 0 {
		return nil, nil
	}
	// Progress is left out: it is recorded against a round, and none has begun.
	opts := s.Review
	opts.ProgressFn = nil
	results, err := review.RunPass(ctx, s.Acquire, s.Release, s.Exec, prompts, opts)
	if err != nil {
		return nil, fmt.Errorf("plan reviews: %w", err)
	}
	var out []review.Finding
	for _, r := range results {
		for _, f := range r.Parsed.Findings {
			f.Prompt = r.Prompt
			out = append(out, f)
		}
	}
	return out, nil
}

// parsePlanMessage reads the plan out of an agent's last message, which the
// schema made the plan object itself.
func parsePlanMessage(msg string) (Plan, error) {
	var p Plan
	if err := json.Unmarshal([]byte(strings.TrimSpace(msg)), &p); err != nil {
		return Plan{}, fmt.Errorf("read the plan: %w", err)
	}
	return validPlan(p)
}

// ParsePlan reads the plan out of claude's JSON result.
func ParsePlan(output string) (Plan, error) {
	p, err := parseStructured[Plan](output, "plan")
	if err != nil {
		return Plan{}, err
	}
	return validPlan(p)
}

// validPlan checks that a plan says what it will do.
func validPlan(p Plan) (Plan, error) {
	p.Summary = strings.TrimSpace(p.Summary)
	if p.Summary == "" {
		return Plan{}, errors.New("the plan has no summary")
	}
	return p, nil
}

// structuredResult is claude's JSON result for a run with --json-schema.
type structuredResult[T any] struct {
	IsError          bool   `json:"is_error"`
	Subtype          string `json:"subtype"`
	Result           string `json:"result"`
	StructuredOutput *T     `json:"structured_output"`
}

// parseStructured reads the schema-checked object out of claude's JSON result;
// what names it in errors.
func parseStructured[T any](output, what string) (T, error) {
	var zero T
	var res structuredResult[T]
	if err := json.Unmarshal([]byte(output), &res); err != nil {
		return zero, fmt.Errorf("read claude's result: %w", err)
	}
	if res.IsError || res.StructuredOutput == nil {
		return zero, fmt.Errorf("claude gave no structured %s (%s): %s",
			what, res.Subtype, review.Tail(strings.TrimSpace(res.Result), 500))
	}
	return *res.StructuredOutput, nil
}

// planPreamble is what the implementer is told before the request: the plan it
// wrote, what its reviewers still disagreed with when it went ahead, and what
// it expected not to be able to verify.
func planPreamble(plan Plan, unresolved []review.Finding) string {
	var b strings.Builder
	b.WriteString("Your plan, reviewed and approved:\n\n")
	b.WriteString(plan.Markdown())
	b.WriteString("\n\nImplement it. If you must deviate, say where and why in your summary.\n")
	if len(unresolved) > 0 {
		b.WriteString("\nThe plan's reviewers still disagreed with it after one revision. Weigh these as you build:\n\n")
		b.WriteString(findingList(unresolved))
	}
	if len(plan.CannotVerifyHere) > 0 {
		b.WriteString("\nYou expected not to be able to verify these in the sandbox:\n")
		for _, c := range plan.CannotVerifyHere {
			fmt.Fprintf(&b, "- %s\n", c)
		}
		b.WriteString("Unless you find a way to verify one, end your summary with it under a " + blockedMarker + " line.\n")
	}
	b.WriteString("\n---\n\n")
	return b.String()
}

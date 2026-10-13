package factory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/smeltdev/smelt/internal/harness"
	"github.com/smeltdev/smelt/internal/review"
)

const (
	// notesTimeout bounds the one attempt at writing a run's notes.
	notesTimeout = 3 * time.Minute
	// maxNotesDiffBytes caps the diff stat in the notes prompt; the writer can
	// read the diff itself.
	maxNotesDiffBytes = 40 * 1024
	// maxNotesOutputBytes caps claude's result, which is a few bullets.
	maxNotesOutputBytes = 64 * 1024
	// maxTitleRunes is the longest commit subject and PR title.
	maxTitleRunes = 72
)

// notesTools limits the notes writer to reading the change.
var notesTools = []string{"Read", "Grep", "Glob", "Bash(git diff:*)", "Bash(git log:*)"}

// NotesSchema is the JSON Schema of the notes claude writes. It is passed with
// --json-schema, so Claude Code validates the answer itself.
const NotesSchema = `{
  "type": "object",
  "properties": {
    "title": {"type": "string", "description": "One line, imperative, at most 72 characters: the commit subject and PR title."},
    "what": {"type": "string", "description": "What changed, as the reader sees it: 1-4 short markdown bullets."},
    "why": {"type": "string", "description": "The problem or goal: 1-3 markdown bullets."},
    "how": {"type": "string", "description": "The approach, naming the key packages or files: 1-4 markdown bullets."}
  },
  "required": ["title", "what", "why", "how"],
  "additionalProperties": false
}`

// Notes is the human-readable account of a run's change.
type Notes struct {
	Title string `json:"title"` // one line, imperative, ≤ 72 chars: the commit subject and PR title
	What  string `json:"what"`  // what changed, as the reader sees it: 1–4 short bullets
	Why   string `json:"why"`   // the problem or goal, 1–3 bullets
	How   string `json:"how"`   // the approach, naming the key packages/files: 1–4 bullets
	// Fallback says why the notes could not be written. When set, the notes
	// are only a title and this reason stands in for What, Why and How.
	Fallback string `json:"fallback,omitempty"`
	// Skipped are the tests the sandbox skipped in the run's last round. They
	// are set from the checks, not written by the model, so a green test gate
	// cannot hide them.
	Skipped []string `json:"skipped,omitempty"`
}

// NotesInput is what the notes writer is told about a run.
type NotesInput struct {
	// Request is the change that was asked for.
	Request string
	Outcome Outcome
	// DiffStat is git's per-file diff stat against the baseline.
	DiffStat string
	// Baseline is the commit the change is measured against.
	Baseline string
	// Plan is the plan the implementer was sent with, nil without one.
	Plan *Plan
	// Summary is the implementer's last turn summary.
	Summary string
	// Log, when set, receives the prompt and the response.
	Log func(heading, body string)
}

// WriteNotes asks the implementer's Claude, which has the change on its
// sidecar, to summarize it. On any failure it returns the fallback notes
// together with the error, so the caller can warn and carry on.
func (s *Sidecars) WriteNotes(ctx context.Context, in NotesInput) (Notes, error) {
	notes, err := s.writeNotes(ctx, in)
	if err != nil {
		return fallbackNotes(in.Request, err.Error()), fmt.Errorf("write the PR notes: %w", err)
	}
	return notes, nil
}

func (s *Sidecars) writeNotes(ctx context.Context, in NotesInput) (Notes, error) {
	im := s.Implementer
	prompt := notesPrompt(in)
	if in.Log != nil {
		in.Log("notes prompt", prompt)
	}
	h := im.agent()
	script := h.ReviewScript(harness.ReviewInput{
		RepoPath: im.Entry.RepoPath, Prompt: prompt, Model: im.Model, Schema: NotesSchema, Tools: notesTools,
	})
	ctx, cancel := context.WithTimeout(ctx, notesTimeout)
	defer cancel()
	out, err := review.RunScriptEnv(ctx, s.Exec, im.Entry, script, h.Env(im.Credential, im.BaseURL), maxNotesOutputBytes)
	if ctx.Err() == context.DeadlineExceeded {
		return Notes{}, fmt.Errorf("timed out after %s", notesTimeout)
	}
	if err != nil {
		return Notes{}, err
	}
	if in.Log != nil {
		in.Log("notes response", out)
	}
	if h.Name() != harness.Claude {
		return parseNotesMessage(out)
	}
	return ParseNotes(out)
}

// notesPrompt tells the writer what the run was for and how it ended.
func notesPrompt(in NotesInput) string {
	var b strings.Builder
	b.WriteString("You are writing the notes for a commit and its pull request. A change was just made and committed in this repository. ")
	b.WriteString("Summarize it for a reviewer who has one minute: what changed, why, and how.\n\n")
	b.WriteString("Rules:\n")
	b.WriteString("- Plain language. No marketing words.\n")
	b.WriteString("- Do not restate the request; say what the change does.\n")
	b.WriteString("- In how, name the key files or packages.\n")
	b.WriteString("- Bullets, not paragraphs. Say nothing about the review process.\n")
	b.WriteString("- title is one imperative line of at most 72 characters.\n\n")
	b.WriteString("The request:\n\n")
	b.WriteString(strings.TrimSpace(in.Request))
	fmt.Fprintf(&b, "\n\nThe run ended: %s", orDefault(string(in.Outcome.Result), "unknown"))
	fmt.Fprintf(&b, " after %d round(s).\n", in.Outcome.Rounds)
	if in.Baseline != "" {
		fmt.Fprintf(&b, "\nThe change is everything since commit %s: read it with `git diff %s HEAD`.\n", in.Baseline, in.Baseline)
	}
	if stat := strings.TrimSpace(in.DiffStat); stat != "" {
		b.WriteString("\ngit diff --stat:\n\n")
		b.WriteString(review.Tail(stat, maxNotesDiffBytes))
		b.WriteString("\n")
	}
	if in.Plan != nil {
		b.WriteString("\nThe plan the implementer was working to, so that what and how stay consistent with it:\n\n")
		b.WriteString(in.Plan.Markdown())
		b.WriteString("\n")
	}
	if s := strings.TrimSpace(in.Summary); s != "" {
		b.WriteString("\nThe implementer's own summary of its last turn:\n\n")
		b.WriteString(s)
		b.WriteString("\n")
	}
	return b.String()
}

// parseNotesMessage reads the notes out of an agent's last message, which the
// schema made the notes object itself rather than claude's JSON result.
func parseNotesMessage(msg string) (Notes, error) {
	var n Notes
	if err := json.Unmarshal([]byte(strings.TrimSpace(msg)), &n); err != nil {
		return Notes{}, fmt.Errorf("read the notes: %w", err)
	}
	return validNotes(n)
}

// ParseNotes reads the notes out of claude's JSON result. Every field must be
// present, and the title is cut to fit a commit subject.
func ParseNotes(output string) (Notes, error) {
	n, err := parseStructured[Notes](output, "notes")
	if err != nil {
		return Notes{}, err
	}
	return validNotes(n)
}

// validNotes cleans n and checks that every field is present.
func validNotes(n Notes) (Notes, error) {
	n.Fallback = ""
	n.Title, n.What, n.Why, n.How = cleanTitle(n.Title), strings.TrimSpace(n.What), strings.TrimSpace(n.Why), strings.TrimSpace(n.How)
	for field, v := range map[string]string{"title": n.Title, "what": n.What, "why": n.Why, "how": n.How} {
		if v == "" {
			return Notes{}, fmt.Errorf("the notes have no %s", field)
		}
	}
	return n, nil
}

// fallbackNotes are the notes of a run whose summary could not be written:
// the request's first line and why there is nothing more. The rest of the
// request is never included.
func fallbackNotes(request, reason string) Notes {
	return Notes{Title: requestTitle(request), Fallback: reason}
}

// ResolveNotes is n, or the fallback notes when a run kept none, such as one
// from before notes were written.
func ResolveNotes(request string, n *Notes) Notes {
	if n != nil {
		return *n
	}
	return fallbackNotes(request, "no summary was kept for this run")
}

// requestTitle is the request's first line as a title.
func requestTitle(request string) string {
	first, _, _ := strings.Cut(strings.TrimSpace(request), "\n")
	return cleanTitle(first)
}

// cleanTitle strips markdown heading markers, which are fragile in commit
// messages, and cuts the line at a word boundary to fit maxTitleRunes.
func cleanTitle(s string) string {
	s = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(s), "#"))
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= maxTitleRunes {
		return s
	}
	cut := []rune(s)[:maxTitleRunes-1]
	for i := len(cut) - 1; i > 0; i-- {
		if unicode.IsSpace(cut[i]) {
			cut = cut[:i]
			break
		}
	}
	return strings.TrimRightFunc(string(cut), func(r rune) bool { return unicode.IsSpace(r) || unicode.IsPunct(r) }) + "…"
}

// sections is the notes' body without headings: the reason when there are no
// notes, else each part under label(name).
func (n Notes) sections(label func(string) string) string {
	parts := make([]string, 0, 4)
	if n.Fallback != "" {
		parts = append(parts, fmt.Sprintf("(smelt could not write a summary for this change: %s)", n.Fallback))
	} else {
		for _, s := range []struct{ name, text string }{{"What", n.What}, {"Why", n.Why}, {"How", n.How}} {
			if s.text != "" {
				parts = append(parts, label(s.name)+"\n"+s.text)
			}
		}
	}
	if len(n.Skipped) > 0 {
		parts = append(parts, "Skipped in the sandbox: "+strings.Join(n.Skipped, ", "))
	}
	return strings.Join(parts, "\n\n")
}

// footer is the run line and the diff stat that close every message.
func footer(runID string, o Outcome, stat string) string {
	line := "Written by smelt factory run " + runID
	if o.Result != "" {
		line += fmt.Sprintf(": %s after %d round(s)", o.Result, o.Rounds)
	}
	line += "."
	if stat != "" {
		line += "\n" + stat
	}
	return line
}

// CommitMessage renders the notes as a commit message. Sections are labelled
// What:, Why: and How: because # lines are fragile in commit messages.
func (n Notes) CommitMessage(runID string, o Outcome, stat string) string {
	parts := []string{n.Title}
	if body := n.sections(func(name string) string { return name + ":" }); body != "" {
		parts = append(parts, body)
	}
	return strings.Join(append(parts, footer(runID, o, stat)), "\n\n") + "\n"
}

// PRBody renders the notes as a PR description.
func (n Notes) PRBody(runID string, o Outcome, stat string) string {
	var b strings.Builder
	if body := n.sections(func(name string) string { return "### " + name }); body != "" {
		b.WriteString(body)
		b.WriteString("\n\n")
	}
	b.WriteString("---\n")
	b.WriteString(footer(runID, o, stat))
	b.WriteString("\n")
	return b.String()
}

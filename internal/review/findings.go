package review

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/CircleCI-Public/chunk-cli/internal/claudecode"
)

// Limits on what is accepted from a model's structured output. They exist
// because the output is untrusted text: a runaway or manipulated review must not
// be able to make the daemon hold or post an unbounded amount.
const (
	// MaxFindings caps the findings kept from one review.
	MaxFindings = 50
	// maxFindingBody caps one finding's explanation, in runes.
	maxFindingBody = 4000
	// maxFindingPatch caps one finding's suggested patch, in bytes.
	maxFindingPatch = 20000
)

// Severities a finding can carry, most serious first.
const (
	SeverityHigh   = "high"
	SeverityMedium = "medium"
	SeverityLow    = "low"
	SeverityInfo   = "info"
)

// Finding is one issue a review reports, in a form a program can act on: where
// it is, how serious it is, what is wrong, and optionally how to fix it.
type Finding struct {
	// ID is unique within a run. The daemon assigns it; a review's own output
	// does not carry one.
	ID string `json:"id,omitempty"`
	// Prompt names the review that reported it.
	Prompt string `json:"prompt,omitempty"`
	// File is a repository-relative path with forward slashes.
	File string `json:"file"`
	// Line is the 1-based line the finding is about; zero means the file as a
	// whole or a line the reviewer did not give.
	Line     int    `json:"line,omitempty"`
	Severity string `json:"severity"`
	Body     string `json:"body"`
	// Patch is an optional unified diff that would fix the finding.
	Patch string `json:"patch,omitempty"`
}

// FindingsSchema is the JSON Schema a review's answer must satisfy when
// structured findings are wanted. It is passed to claude with --json-schema, so
// Claude Code validates the answer itself and retries until it conforms; the
// prompt is left as written.
const FindingsSchema = `{
  "type": "object",
  "properties": {
    "review": {
      "type": "string",
      "description": "Your review as prose, written for the developer who made the change."
    },
    "findings": {
      "type": "array",
      "description": "Issues in the code under review, and nothing else. Empty when there is nothing to report.",
      "items": {
        "type": "object",
        "properties": {
          "file": {"type": "string", "description": "Path relative to the repository root, with forward slashes."},
          "line": {"type": "integer", "minimum": 0, "description": "1-based line the finding is about; 0 for the file as a whole."},
          "severity": {"type": "string", "enum": ["high", "medium", "low", "info"]},
          "body": {"type": "string", "description": "What is wrong and why."},
          "patch": {"type": "string", "description": "Optional unified diff that fixes it."}
        },
        "required": ["file", "severity", "body"],
        "additionalProperties": false
      }
    }
  },
  "required": ["review", "findings"],
  "additionalProperties": false
}`

// Parsed is the result of reading structured findings out of a review's output.
type Parsed struct {
	// Findings are the valid findings, in the order given, at most MaxFindings.
	Findings []Finding
	// Dropped counts entries that were present but unusable (no file, no body, a
	// path outside the repository) or over the cap.
	Dropped int
	// Prose is the review's prose.
	Prose string
}

// structuredFindings is a review's answer as FindingsSchema shapes it.
type structuredFindings struct {
	Review   string       `json:"review"`
	Findings []rawFinding `json:"findings"`
}

// rawFinding is a finding as the review gave it, before it is checked.
type rawFinding struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Severity string `json:"severity"`
	Body     string `json:"body"`
	Patch    string `json:"patch"`
}

// maxResultInError caps how much of claude's own result text an error quotes.
const maxResultInError = 500

// ParseFindings reads the findings out of claude's JSON result. Claude Code has
// already checked the shape against FindingsSchema; what is left is the content,
// which is still untrusted: paths are confined to the repository and sizes are
// capped. A result with no structured output is an error, not a prose-only
// review.
func ParseFindings(output string) (Parsed, error) {
	res, err := claudecode.ParseResult(output)
	if err != nil {
		return Parsed{}, err
	}
	if res.IsError || res.StructuredOutput == nil {
		return Parsed{}, fmt.Errorf("claude gave no structured findings (%s): %s",
			res.Subtype, truncateRunes(strings.TrimSpace(res.Result), maxResultInError))
	}
	var answer structuredFindings
	if err := json.Unmarshal(res.StructuredOutput, &answer); err != nil {
		return Parsed{}, fmt.Errorf("read claude's structured findings: %w", err)
	}
	findings, dropped := cleanFindings(answer.Findings)
	return Parsed{
		Findings: findings,
		Dropped:  dropped,
		Prose:    strings.TrimSpace(answer.Review),
	}, nil
}

func cleanFindings(raw []rawFinding) ([]Finding, int) {
	var out []Finding
	dropped := 0
	for _, r := range raw {
		if len(out) >= MaxFindings {
			dropped++
			continue
		}
		f, ok := cleanFinding(r)
		if !ok {
			dropped++
			continue
		}
		out = append(out, f)
	}
	return out, dropped
}

func cleanFinding(r rawFinding) (Finding, bool) {
	file, ok := cleanRepoPath(r.File)
	body := truncateRunes(strings.TrimSpace(r.Body), maxFindingBody)
	if !ok || body == "" {
		return Finding{}, false
	}
	patch := r.Patch
	if len(patch) > maxFindingPatch {
		// A truncated diff would not apply; better none than a broken one.
		patch = ""
	}
	return Finding{
		File:     file,
		Line:     max(r.Line, 0),
		Severity: normalizeSeverity(r.Severity),
		Body:     body,
		Patch:    patch,
	}, true
}

// cleanRepoPath accepts only a path inside the repository. Findings become
// review comments and edit targets, so a path naming somewhere else (absolute,
// or climbing out with "..") is refused rather than passed on.
func cleanRepoPath(p string) (string, bool) {
	p = strings.TrimSpace(strings.ReplaceAll(p, "\\", "/"))
	p = strings.TrimPrefix(p, "./")
	if p == "" || strings.HasPrefix(p, "/") || strings.ContainsRune(p, 0) {
		return "", false
	}
	clean := path.Clean(p)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", false
	}
	return clean, true
}

// normalizeSeverity keeps the four levels and makes anything else info: the
// schema already restricts severity, but a finding of unknown weight should
// never be promoted to something that looks urgent.
func normalizeSeverity(s string) string {
	switch s {
	case SeverityHigh, SeverityMedium, SeverityLow:
		return s
	}
	return SeverityInfo
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

// Location is where the finding is, as file:line, or the file alone when the
// finding has no line.
func (f Finding) Location() string {
	if f.Line > 0 {
		return fmt.Sprintf("%s:%d", f.File, f.Line)
	}
	return f.File
}

// WorthChanging reports whether a finding is serious enough to act on: severity
// high or medium. Lower findings are kept on the record but never trigger a fix,
// so a loop does not churn the code over style remarks.
func (f Finding) WorthChanging() bool {
	return f.Severity == SeverityHigh || f.Severity == SeverityMedium
}

// DedupeFindings drops repeats: several reviews of the same change often flag
// the same line for the same reason, and it need not be fixed twice. The first
// copy's ID is kept, at the most serious severity any copy had.
func DedupeFindings(findings []Finding) []Finding {
	type key struct {
		file string
		line int
		body string
	}
	rank := map[string]int{SeverityHigh: 0, SeverityMedium: 1, SeverityLow: 2, SeverityInfo: 3}
	index := map[key]int{}
	var out []Finding
	for _, f := range findings {
		k := key{f.File, f.Line, strings.ToLower(strings.Join(strings.Fields(f.Body), " "))}
		if i, seen := index[k]; seen {
			if rank[f.Severity] < rank[out[i].Severity] {
				out[i].Severity = f.Severity
			}
			continue
		}
		index[k] = len(out)
		out = append(out, f)
	}
	return out
}

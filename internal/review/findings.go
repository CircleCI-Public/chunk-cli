package review

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Severity ranks how much a finding matters.
type Severity string

// Severities, most severe first.
const (
	SeverityCritical Severity = "critical"
	SeverityHigh     Severity = "high"
	SeverityMedium   Severity = "medium"
	SeverityLow      Severity = "low"
)

// Finding is one issue a review reports. Findings are data rather than prose
// so they can be filtered, deduplicated and verified across reviews and passes.
type Finding struct {
	File string `json:"file"`
	// Line is 1-based; 0 means the finding is about the file as a whole.
	Line            int      `json:"line"`
	Severity        Severity `json:"severity"`
	Confidence      int      `json:"confidence"` // 0-100
	Claim           string   `json:"claim"`
	FailureScenario string   `json:"failure_scenario"`
}

// findingsSchema is the JSON Schema claude's structured output must match.
// The descriptions are the only instructions reviewers get about the shape of
// their answer, so the prompt files stay about what to review.
const findingsSchema = `{
  "type": "object",
  "properties": {
    "summary": {
      "type": "string",
      "description": "One or two sentences on the overall state of the changes. Say so plainly when nothing was found."
    },
    "findings": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "file": {"type": "string", "description": "Path relative to the repository root."},
          "line": {"type": "integer", "minimum": 0, "description": "1-based line in the file as it is on disk, or 0 when the finding is about the file as a whole."},
          "severity": {"type": "string", "enum": ["critical", "high", "medium", "low"]},
          "confidence": {"type": "integer", "minimum": 0, "maximum": 100, "description": "How sure you are that this is a real defect, from 0 to 100."},
          "claim": {"type": "string", "description": "One sentence stating the defect."},
          "failure_scenario": {"type": "string", "description": "The concrete input or state that triggers it, and what goes wrong."}
        },
        "required": ["file", "line", "severity", "confidence", "claim", "failure_scenario"],
        "additionalProperties": false
      }
    }
  },
  "required": ["summary", "findings"],
  "additionalProperties": false
}`

// report is a review's structured output.
type report struct {
	Summary  string    `json:"summary"`
	Findings []Finding `json:"findings"`
}

// envelope is the part of claude's --output-format json result a review reads.
type envelope struct {
	Subtype          string          `json:"subtype"`
	IsError          bool            `json:"is_error"`
	Result           string          `json:"result"`
	StructuredOutput json.RawMessage `json:"structured_output"`
}

// errNoStructuredOutput is returned when claude finishes without an answer
// matching findingsSchema.
var errNoStructuredOutput = errors.New("review returned no structured output")

// parseEnvelope decodes claude's JSON result. It fails for output that is not
// one, such as plain text from a claude too old for JSON output.
func parseEnvelope(stdout string) (envelope, error) {
	var env envelope
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		return envelope{}, fmt.Errorf("decode claude result: %w", err)
	}
	return env, nil
}

// parseReport extracts the review from a successful claude run's stdout.
func parseReport(stdout string) (report, error) {
	env, err := parseEnvelope(stdout)
	if err != nil {
		return report{}, err
	}
	if env.IsError {
		msg := strings.TrimSpace(env.Result)
		// A rejected credential can arrive as a result with exit code 0, so
		// it is checked here as well as on the exit code.
		if credentialRejectedRe.MatchString(msg) {
			return report{}, ErrCredentialRejected
		}
		// Claude gives API errors the subtype "success", which reads wrongly
		// after "reported".
		what := env.Subtype
		if what == "" || what == "success" {
			what = "an error"
		}
		if msg == "" {
			return report{}, fmt.Errorf("claude reported %s", what)
		}
		return report{}, fmt.Errorf("claude reported %s: %s", what, msg)
	}
	if len(env.StructuredOutput) == 0 || string(env.StructuredOutput) == "null" {
		return report{}, errNoStructuredOutput
	}
	var r report
	if err := json.Unmarshal(env.StructuredOutput, &r); err != nil {
		return report{}, fmt.Errorf("decode review: %w", err)
	}
	if r.Findings == nil {
		r.Findings = []Finding{}
	}
	for i, f := range r.Findings {
		if err := f.validate(); err != nil {
			return report{}, fmt.Errorf("finding %d: %w", i+1, err)
		}
	}
	return r, nil
}

// validate checks the constraints findingsSchema asks claude to meet, so a
// value outside them is reported as a failed review rather than ranked or
// printed as if it were sound.
func (f Finding) validate() error {
	switch f.Severity {
	case SeverityCritical, SeverityHigh, SeverityMedium, SeverityLow:
	default:
		return fmt.Errorf("unknown severity %q", f.Severity)
	}
	if f.Confidence < 0 || f.Confidence > 100 {
		return fmt.Errorf("confidence %d is outside 0-100", f.Confidence)
	}
	if f.Line < 0 {
		return fmt.Errorf("line %d is negative", f.Line)
	}
	return nil
}

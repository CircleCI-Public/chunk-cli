package factory

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Verdict is a reviewer's judgement of one attempt.
type Verdict string

// Review verdicts, from worst to best.
const (
	VerdictBlocked  Verdict = "blocked"
	VerdictWarn     Verdict = "warn"
	VerdictApproved Verdict = "approved"
)

// ReviewSchema is the JSON Schema every reviewer's answer must match. Claude
// enforces it through --json-schema, so a verdict is never scraped from prose.
const ReviewSchema = `{"type":"object","properties":{"verdict":{"type":"string","enum":["blocked","warn","approved"]},"feedback":{"type":"string"}},"required":["verdict","feedback"],"additionalProperties":false}`

// ReviewPrompt wraps a review prompt with the intent being implemented, the
// commit the work started from, and the instructions that go with ReviewSchema.
func ReviewPrompt(body, intent, base string) string {
	return fmt.Sprintf(`%s

## What is being implemented

Every change since commit %s was made to implement the intent below. Review
those changes against it: start with 'git diff %s' and 'git log %s..HEAD'.

<intent>
%s
</intent>

## How to answer

Return a verdict and your feedback:
- "blocked": the changes must not ship until the issues are fixed.
- "warn": the changes could ship, but have issues worth fixing.
- "approved": nothing needs to change.

Feedback must be specific and actionable: name files and lines. Leave it
empty only when approving.`, body, base, base, base, intent)
}

// claudeResult is the part of claude's --output-format json envelope a review
// reads. --json-schema puts the answer in structured_output.
type claudeResult struct {
	IsError          bool            `json:"is_error"`
	Result           string          `json:"result"`
	StructuredOutput json.RawMessage `json:"structured_output"`
}

type reviewAnswer struct {
	Verdict  Verdict `json:"verdict"`
	Feedback string  `json:"feedback"`
}

// ParseReview reads the verdict and feedback out of a reviewer's JSON output.
func ParseReview(output string) (Verdict, string, error) {
	var env claudeResult
	if err := json.Unmarshal([]byte(output), &env); err != nil {
		return "", "", fmt.Errorf("parse claude output: %w", err)
	}
	if env.IsError {
		return "", "", fmt.Errorf("claude reported an error: %s", env.Result)
	}
	var ans reviewAnswer
	if err := json.Unmarshal(env.StructuredOutput, &ans); err != nil {
		return "", "", fmt.Errorf("parse review answer: %w", err)
	}
	switch ans.Verdict {
	case VerdictBlocked, VerdictWarn, VerdictApproved:
		return ans.Verdict, strings.TrimSpace(ans.Feedback), nil
	case "":
		return "", "", errors.New("review answer has no verdict")
	default:
		return "", "", fmt.Errorf("unknown verdict %q", ans.Verdict)
	}
}

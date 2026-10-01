package review

import (
	"encoding/json"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

// claudeJSON renders claude's --output-format json result around a structured
// answer, the way claude does: the answer twice, as text and as an object.
func claudeJSON(t *testing.T, structured any) string {
	t.Helper()
	text, err := json.Marshal(structured)
	assert.NilError(t, err)
	raw, err := json.Marshal(map[string]any{
		"type": "result", "subtype": "success", "is_error": false,
		"result": string(text), "structured_output": structured,
	})
	assert.NilError(t, err)
	return string(raw)
}

func TestParseFindingsReadsTheStructuredOutput(t *testing.T) {
	out := claudeJSON(t, map[string]any{
		"review": "  The change looks mostly fine.\n\nTwo issues below.\n",
		"findings": []map[string]any{
			{"file": "internal/a.go", "line": 12, "severity": "high", "body": "nil deref", "patch": "--- a/internal/a.go\n+++ b/internal/a.go\n@@ -1 +1 @@\n-x\n+y\n"},
			{"file": "./b.go", "line": 7, "severity": "medium", "body": "unchecked error"},
		},
	})

	got, err := ParseFindings(out)

	assert.NilError(t, err)
	assert.Equal(t, got.Dropped, 0)
	assert.Equal(t, len(got.Findings), 2)
	assert.Equal(t, got.Findings[0].File, "internal/a.go")
	assert.Equal(t, got.Findings[0].Line, 12)
	assert.Equal(t, got.Findings[0].Severity, SeverityHigh)
	assert.Assert(t, strings.Contains(got.Findings[0].Patch, "+y"))
	assert.Equal(t, got.Findings[1].File, "b.go", "leading ./ is dropped")
	assert.Equal(t, got.Findings[1].Severity, SeverityMedium)
	assert.Equal(t, got.Prose, "The change looks mostly fine.\n\nTwo issues below.")
}

// A patch that contains a code fence is just a string in the structured
// output; nothing has to find where a block ends.
func TestParseFindingsKeepsAPatchContainingAFence(t *testing.T) {
	patch := "+```go\n+x := 1\n+```\n"
	out := claudeJSON(t, map[string]any{
		"review":   "Prose.",
		"findings": []map[string]any{{"file": "README.md", "line": 3, "severity": "low", "body": "add a fence", "patch": patch}},
	})

	got, err := ParseFindings(out)

	assert.NilError(t, err)
	assert.Equal(t, len(got.Findings), 1)
	assert.Equal(t, got.Findings[0].Patch, patch)
}

func TestParseFindingsFailsWithoutStructuredOutput(t *testing.T) {
	for name, tc := range map[string]struct{ out, want string }{
		"not json": {out: "Just prose.", want: "read claude's result"},
		"error result": {
			out:  `{"type":"result","subtype":"error_max_structured_output_retries","is_error":true,"result":"gave up"}`,
			want: "claude gave no structured findings (error_max_structured_output_retries): gave up",
		},
		"no structured output": {
			out:  `{"type":"result","subtype":"success","is_error":false,"result":"prose only"}`,
			want: "claude gave no structured findings (success): prose only",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseFindings(tc.out)
			assert.ErrorContains(t, err, tc.want)
		})
	}
}

func TestParseFindingsDropsUnusableEntriesAndCountsThem(t *testing.T) {
	out := claudeJSON(t, map[string]any{
		"review": "",
		"findings": []map[string]any{
			{"file": "", "body": "no file"},
			{"file": "a.go", "body": "   "},
			{"file": "/etc/passwd", "body": "absolute"},
			{"file": "../outside.go", "body": "climbs out"},
			{"file": "a/../../b.go", "body": "climbs out later"},
			{"file": "ok.go", "body": "fine", "severity": "catastrophic", "line": -3},
		},
	})

	got, err := ParseFindings(out)

	assert.NilError(t, err)
	assert.Equal(t, got.Dropped, 5)
	assert.Equal(t, len(got.Findings), 1)
	assert.Equal(t, got.Findings[0].File, "ok.go")
	assert.Equal(t, got.Findings[0].Severity, SeverityInfo, "unknown severity is not promoted")
	assert.Equal(t, got.Findings[0].Line, 0, "a negative line is no line")
}

func TestFindingsSchemaIsValidJSON(t *testing.T) {
	var schema map[string]any
	assert.NilError(t, json.Unmarshal([]byte(FindingsSchema), &schema))
	assert.Equal(t, schema["type"], "object")
}

func TestWorthChangingIsHighAndMediumOnly(t *testing.T) {
	for sev, want := range map[string]bool{SeverityHigh: true, SeverityMedium: true, SeverityLow: false, SeverityInfo: false} {
		assert.Equal(t, Finding{Severity: sev}.WorthChanging(), want, sev)
	}
}

// Two reviews flagging one line say it once, at the most serious level either gave it.
func TestDedupeFindingsKeepsTheFirstIDAndTheHighestSeverity(t *testing.T) {
	got := DedupeFindings([]Finding{
		{ID: "a-1", File: "x.go", Line: 3, Severity: SeverityLow, Body: "Nil  dereference"},
		{ID: "b-1", File: "x.go", Line: 3, Severity: SeverityHigh, Body: "nil dereference"},
		{ID: "b-2", File: "x.go", Line: 4, Severity: SeverityLow, Body: "nil dereference"},
	})

	assert.Equal(t, len(got), 2)
	assert.Equal(t, got[0].ID, "a-1")
	assert.Equal(t, got[0].Severity, SeverityHigh)
}

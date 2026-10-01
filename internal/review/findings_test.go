package review

import (
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

const goodBlock = "```json\n" + `{"findings":[
  {"file":"internal/a.go","line":12,"severity":"high","body":"nil deref","patch":"--- a/internal/a.go\n+++ b/internal/a.go\n@@ -1 +1 @@\n-x\n+y\n"},
  {"file":"./b.go","line":"7","severity":"Warning","body":"unchecked error"}
]}` + "\n```"

func TestParseFindingsReadsTheJSONBlockAndKeepsTheProse(t *testing.T) {
	out := "The change looks mostly fine.\n\nTwo issues below.\n\n" + goodBlock + "\n"

	got := ParseFindings(out)

	assert.Assert(t, got.Found)
	assert.Equal(t, got.Dropped, 0)
	assert.Equal(t, len(got.Findings), 2)
	assert.Equal(t, got.Findings[0].File, "internal/a.go")
	assert.Equal(t, got.Findings[0].Line, 12)
	assert.Equal(t, got.Findings[0].Severity, SeverityHigh)
	assert.Assert(t, strings.Contains(got.Findings[0].Patch, "+y"))
	assert.Equal(t, got.Findings[1].File, "b.go", "leading ./ is dropped")
	assert.Equal(t, got.Findings[1].Line, 7, "a numeric string is accepted as a line")
	assert.Equal(t, got.Findings[1].Severity, SeverityMedium, "warning maps to medium")
	assert.Equal(t, got.Prose, "The change looks mostly fine.\n\nTwo issues below.")
	assert.Assert(t, !strings.Contains(got.Prose, "findings"))
}

func TestParseFindingsWithNoBlockIsProseOnlyNotAnError(t *testing.T) {
	out := "Looks good to me. No JSON here."

	got := ParseFindings(out)

	assert.Assert(t, !got.Found)
	assert.Equal(t, len(got.Findings), 0)
	assert.Equal(t, got.Prose, out)
}

func TestParseFindingsToleratesMalformedJSON(t *testing.T) {
	for name, out := range map[string]string{
		"truncated":   "review\n```json\n{\"findings\":[{\"file\":\"a.go\",\n```",
		"not json":    "review\n```json\nthis is not json\n```",
		"unclosed":    "review\n```json\n{\"findings\":[]}",
		"wrong shape": "review\n```json\n{\"name\":\"a config example\"}\n```",
		"scalar":      "review\n```json\n42\n```",
	} {
		t.Run(name, func(t *testing.T) {
			got := ParseFindings(out)
			assert.Assert(t, !got.Found)
			assert.Equal(t, got.Prose, out, "prose must be untouched when nothing parsed")
		})
	}
}

func TestParseFindingsTakesTheLastDecodableBlock(t *testing.T) {
	out := "first try\n```json\n{\"findings\":[{\"file\":\"old.go\",\"body\":\"old\"}]}\n```\n" +
		"revised\n```json\n{\"findings\":[{\"file\":\"new.go\",\"body\":\"new\"}]}\n```\n" +
		"and a quoted example\n```json\n{\"unrelated\":true}\n```"

	got := ParseFindings(out)

	assert.Assert(t, got.Found)
	assert.Equal(t, len(got.Findings), 1)
	assert.Equal(t, got.Findings[0].File, "new.go")
}

func TestParseFindingsDropsUnusableEntriesAndCountsThem(t *testing.T) {
	out := "```json\n" + `{"findings":[
	  {"file":"","body":"no file"},
	  {"file":"a.go","body":"   "},
	  {"file":"/etc/passwd","body":"absolute"},
	  {"file":"../outside.go","body":"climbs out"},
	  {"file":"a/../../b.go","body":"climbs out later"},
	  {"file":"ok.go","body":"fine","severity":"catastrophic","line":-3}
	]}` + "\n```"

	got := ParseFindings(out)

	assert.Assert(t, got.Found)
	assert.Equal(t, got.Dropped, 5)
	assert.Equal(t, len(got.Findings), 1)
	assert.Equal(t, got.Findings[0].File, "ok.go")
	assert.Equal(t, got.Findings[0].Severity, SeverityInfo, "unknown severity is not promoted")
	assert.Equal(t, got.Findings[0].Line, 0, "a negative line is no line")
}

// A suggested patch that itself contains a code fence looks, to a fence scanner,
// like the end of the block. The findings must survive it.
func TestParseFindingsSurvivesAFenceInsideAJSONString(t *testing.T) {
	out := "Prose.\n```json\n" +
		`{"findings":[{"file":"README.md","line":3,"severity":"low","body":"add a fence","patch":"+` + "```" + `go\n+x := 1\n+` + "```" + `\n"}]}` +
		"\n```\nTrailing words.\n"

	got := ParseFindings(out)

	assert.Assert(t, got.Found, "prose: %q", got.Prose)
	assert.Equal(t, len(got.Findings), 1)
	assert.Equal(t, got.Findings[0].File, "README.md")
	assert.Assert(t, strings.Contains(got.Findings[0].Patch, "x := 1"))
	assert.Equal(t, got.Prose, "Prose.\n\nTrailing words.")
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

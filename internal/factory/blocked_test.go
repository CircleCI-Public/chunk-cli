package factory

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/smeltdev/smelt/internal/harness"
	"github.com/smeltdev/smelt/internal/harness/codex"
	"github.com/smeltdev/smelt/internal/review"
)

func TestImplementerPromptDemandsTheWholeRequest(t *testing.T) {
	p := implementerSystemPrompt("")
	for _, want := range []string{"everything the request asks for", "required work", "BLOCKED: <what and why>"} {
		assert.Assert(t, strings.Contains(p, want), "missing %q in %s", want, p)
	}
}

func TestBlockedReason(t *testing.T) {
	assert.Equal(t, BlockedReason("Did it.\nBLOCKED: needs a token"), "needs a token")
	assert.Equal(t, BlockedReason("**BLOCKED:** needs a token"), "needs a token")
	assert.Equal(t, BlockedReason("nothing is blocked: all done"), "")
}

func missingFinding() review.Finding {
	return review.Finding{File: "a.go", Severity: "medium", Body: "Nothing outside the factory can select a harness. The CLI is missing."}
}

func TestFeedbackSaysWhenTheRequestIsIncomplete(t *testing.T) {
	const line = "These findings say the request is not complete. Implement the missing parts this round."
	incomplete := Check{Name: "adversarial", Kind: KindReview, Status: StatusFailed, Feedback: "x", Findings: []review.Finding{missingFinding()}}
	assert.Assert(t, strings.Contains(Feedback([]Check{incomplete}), line))
	incomplete.Name = "requirements"
	assert.Assert(t, strings.Contains(Feedback([]Check{incomplete}), line))

	other := Check{Name: "correctness", Kind: KindReview, Status: StatusFailed, Feedback: "x", Findings: []review.Finding{missingFinding()}}
	assert.Assert(t, !strings.Contains(Feedback([]Check{other}), line))
	bug := Check{Name: "adversarial", Kind: KindReview, Status: StatusFailed, Feedback: "x",
		Findings: []review.Finding{{File: "a.go", Severity: "high", Body: "nil deref"}}}
	assert.Assert(t, !strings.Contains(Feedback([]Check{bug}), line))
}

func TestLoopBlockedSummary(t *testing.T) {
	steps := &fakeSteps{changes: []Change{change(1), change(1)}, checks: [][]Check{{fail}}}
	steps.summary = "done\nBLOCKED: needs a credential"
	out, err := Loop{Attempts: 5}.Run(context.Background(), steps, "p")
	assert.NilError(t, err)
	assert.Equal(t, out.Result, ResultBlocked)
	assert.Equal(t, out.Blocked, "needs a credential")

	steps = &fakeSteps{changes: []Change{change(1), change(1)}, checks: [][]Check{{fail}}}
	out, err = Loop{Attempts: 5}.Run(context.Background(), steps, "p")
	assert.NilError(t, err)
	assert.Equal(t, out.Result, ResultStuck)
}

func TestOutcomeIncomplete(t *testing.T) {
	o := Outcome{Checks: []Check{{Name: "adversarial", Kind: KindReview, Status: StatusFailed, Findings: []review.Finding{missingFinding()}}}}
	got := o.Incomplete()
	assert.Equal(t, len(got), 1)
	assert.Equal(t, FindingTitle(got[0]), "Nothing outside the factory can select a harness")
}

func TestCodexRulesDemandTheWholeRequest(t *testing.T) {
	script := codex.Harness{}.ImplementScript(harness.ImplementInput{RepoPath: "/r", Prompt: "do it", SystemPrompt: implementerSystemPrompt("")})
	_, line, _ := strings.Cut(script, "echo ")
	enc, _, _ := strings.Cut(line, " ")
	b, err := base64.StdEncoding.DecodeString(enc)
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(string(b), "everything the request asks for"), string(b))
	assert.Assert(t, strings.Contains(string(b), "BLOCKED: <what and why>"), string(b))
}

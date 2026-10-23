package factory

import (
	"fmt"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

// The plan's headings are factory's (plugin/planning.md in circleci/factory),
// whose tasks find each part of a plan by its heading.
func TestPlanSystemPrompt_usesFactoryHeadings(t *testing.T) {
	prompt := fmt.Sprintf(planSystemPrompt, "/tmp/plan.md")
	for _, h := range []string{
		"## 1. Outcome", "## 2. Design", "### Context", "### Spec", "### Decisions",
		"### Out of scope", "## 3. Open Questions", "## 4. Worked example", "## 5. Units",
	} {
		assert.Check(t, strings.Contains(prompt, "\n"+h+"\n"), "missing heading %q", h)
	}
	assert.Check(t, !strings.Contains(prompt, "Spec/Decisions/Scope"))
	assert.Check(t, strings.Contains(prompt, "/tmp/plan.md"))
	assert.Check(t, !strings.Contains(prompt, "%!"), "the prompt takes exactly one %%s")
	assert.Check(t, strings.Contains(prompt, `starts with a "# " title line naming the change in a few lowercase words joined by hyphens`),
		"missing instruction for the plan's title line")
}

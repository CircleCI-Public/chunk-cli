package factory

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

func TestTemplatesListsTheStandardSets(t *testing.T) {
	assert.DeepEqual(t, Templates(), []string{"generic", "go-service", "react"})
}

// Every standard prompt must hold to the same rules bootstrap asks Claude to
// follow, since both end up in .chunk/reviews and drive the same loop.
func TestTemplatesFollowTheReviewPromptRules(t *testing.T) {
	for _, name := range Templates() {
		t.Run(name, func(t *testing.T) {
			prompts, err := Template(name)
			assert.NilError(t, err)
			assert.Assert(t, len(prompts) >= 2 && len(prompts) <= maxBootstrapPrompts,
				"%d prompts", len(prompts))
			for _, p := range prompts {
				assert.Assert(t, promptNameRe.MatchString(p.Name), "name %q", p.Name)
				assert.Assert(t, len(p.Body) <= maxPromptBody, "%s is %d bytes", p.Name, len(p.Body))
				assert.Assert(t, strings.Count(p.Body, "\n") < 80, "%s is over 80 lines", p.Name)
				for _, want := range []string{
					"## Severity", "- **high**:", "- **medium**:", "- **low**:",
					"## Scope", "Review only the change", "in one pass",
					".chunk/context/review-prompt.md",
				} {
					assert.Assert(t, strings.Contains(p.Body, want), "%s has no %q", p.Name, want)
				}
			}
		})
	}
}

func TestTemplateIsReadyToWrite(t *testing.T) {
	prompts, err := Template("go-service")
	assert.NilError(t, err)
	dir := filepath.Join(t.TempDir(), "reviews")
	paths, err := WritePrompts(dir, prompts)
	assert.NilError(t, err)
	assert.DeepEqual(t, paths, []string{
		filepath.Join(dir, "api-and-security.md"),
		filepath.Join(dir, "correctness.md"),
		filepath.Join(dir, "testing.md"),
	})
}

func TestTemplateRejectsUnknownNames(t *testing.T) {
	for _, name := range []string{"rails", "", "../templates", "go-service/testing.md"} {
		_, err := Template(name)
		assert.Assert(t, errors.Is(err, ErrUnknownTemplate), "%q: %v", name, err)
	}
}

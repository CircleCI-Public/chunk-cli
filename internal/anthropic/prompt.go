package anthropic

import (
	"context"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
)

// GenerateReviewPrompt uses Claude to generate a PR review prompt from analysis.
func (c *Client) GenerateReviewPrompt(ctx context.Context, analysis, model string, includeAttribution bool) (string, error) {
	if model == "" {
		model = config.PromptModel
	}
	prompt := buildPromptGenerationPrompt(analysis, includeAttribution)
	return c.Ask(ctx, model, 8000, prompt)
}

package anthropic

import (
	"context"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
)

// AnalyzeReviews sends review data to Claude for pattern analysis.
func (c *Client) AnalyzeReviews(ctx context.Context, prompt, model string) (string, error) {
	if model == "" {
		model = config.AnalyzeModel
	}
	return c.sendMessage(ctx, model, 16000, prompt)
}

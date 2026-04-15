package sandbox

import (
	"context"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
)

func Reset(ctx context.Context, client *circleci.Client, sandboxID string) (*circleci.ResetSandboxResponse, error) {
	return client.ResetSandbox(ctx, sandboxID)
}

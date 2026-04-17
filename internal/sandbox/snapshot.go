package sandbox

import (
	"context"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
)

func CreateSnapshot(ctx context.Context, client *circleci.Client, sandboxID, name string) (*circleci.Snapshot, error) {
	return client.CreateSnapshot(ctx, sandboxID, name)
}

func GetSnapshot(ctx context.Context, client *circleci.Client, id string) (*circleci.Snapshot, error) {
	return client.GetSnapshot(ctx, id)
}

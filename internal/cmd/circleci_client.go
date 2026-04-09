package cmd

import (
	"fmt"
	"os"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
)

// newCircleCIClient resolves a CircleCI token (env vars then config file) and
// returns a ready-to-use client. Token resolution and base URL reading belong
// in the cmd layer, not in the circleci leaf package.
func newCircleCIClient() (*circleci.Client, error) {
	token := os.Getenv("CIRCLE_TOKEN")
	if token == "" {
		token = os.Getenv("CIRCLECI_TOKEN")
	}
	if token == "" {
		cfg, _ := config.Load()
		token = cfg.CircleCIToken
	}
	if token == "" {
		return nil, fmt.Errorf("circleci token not found: set CIRCLE_TOKEN or run 'chunk auth set'")
	}
	return circleci.NewClientWithToken(token, os.Getenv("CIRCLECI_BASE_URL"))
}

package task

import (
	"context"
	"fmt"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
)

type RunParams struct {
	Definition     string
	Prompt         string
	Branch         string
	NewBranch      bool
	PipelineAsTool bool
}

func TriggerRun(ctx context.Context, client *circleci.Client, cfg *RunConfig, params RunParams) (*circleci.RunResponse, error) {
	defID, envID, defaultBranch, err := GetDefinitionByNameOrID(cfg, params.Definition)
	if err != nil {
		return nil, err
	}

	branch := defaultBranch
	if params.Branch != "" {
		branch = params.Branch
	}

	body := circleci.TriggerRunRequest{
		AgentType:          "prompt",
		DefinitionID:       defID,
		CheckoutBranch:     branch,
		TriggerSource:      "chunk-cli",
		ChunkEnvironmentID: envID,
		Parameters: map[string]interface{}{
			"agent-type":             "prompt",
			"custom-prompt":          params.Prompt,
			"run-pipeline-as-a-tool": params.PipelineAsTool,
			"create-new-branch":      params.NewBranch,
		},
		Stats: &circleci.TriggerRunStats{
			Prompt:         params.Prompt,
			CheckoutBranch: branch,
		},
	}

	resp, err := client.TriggerRun(ctx, cfg.OrgID, cfg.ProjectID, body)
	if err != nil {
		return nil, fmt.Errorf("trigger run error: %w", err)
	}
	return resp, nil
}
